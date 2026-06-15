package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/workshop/internal/app"
	"github.com/andrewhowdencom/workshop/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

func init() {
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().String("provider.kind", "openai", "Provider kind (e.g. openai)")
	rootCmd.PersistentFlags().String("provider.api-key", "", "API key for the provider")
	rootCmd.PersistentFlags().String("provider.model", "gpt-4o", "Model name (e.g. gpt-4o)")
	rootCmd.PersistentFlags().String("provider.base-url", "", "Custom API base URL")
	rootCmd.PersistentFlags().Float64("provider.temperature", 0, "Sampling temperature for the provider (0 = default)")
	rootCmd.PersistentFlags().String("provider.thinking-level", "", "Thinking effort level: off, minimal, low, medium, high, max. Default: off (openai-compatible), medium (anthropic).")
	rootCmd.PersistentFlags().Int64("provider.max-tokens", 0, "Maximum output tokens per request (anthropic only; 0 = use provider default of 32000)")
	rootCmd.PersistentFlags().String("store.dir", "", "Directory for persistent JSON thread storage (default: $XDG_DATA_HOME/workshop/threads)")
	rootCmd.PersistentFlags().String("role", "", "Initial role for new threads")
	rootCmd.PersistentFlags().Bool("pprof", false, "Enable the pprof debug server")
	rootCmd.PersistentFlags().String("pprof.addr", defaultPProfAddr, "TCP address for the pprof server")
	rootCmd.PersistentFlags().String("telemetry.traces.endpoint", "", "OpenTelemetry OTLP/HTTP endpoint URL for traces (e.g. http://localhost:4318); empty = disabled")
	rootCmd.PersistentFlags().String("telemetry.metrics.endpoint", "", "OpenTelemetry OTLP/HTTP endpoint URL for metrics (e.g. http://localhost:4318); empty = disabled")
	rootCmd.PersistentFlags().Int("compaction.max-tokens", 100000, "Trigger compaction when total tokens exceed this threshold (0 = disabled)")

	rootCmd.Flags().String("thread", "", "Existing thread UUID to resume")

	setupViper(viper.GetViper())
	if err := loadViperConfig(viper.GetViper()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	cobra.CheckErr(viper.BindPFlags(rootCmd.PersistentFlags()))
	cobra.CheckErr(viper.BindPFlags(rootCmd.Flags()))
}

var rootCmd = &cobra.Command{
	Use:   "workshop",
	Short: "A terminal-based coding assistant",
	Long: `A terminal-based coding assistant built on the ore framework.
It wires together the TUI conduit, system prompt transforms, guardrails,
filesystem tools, and a bash execution tool to create an interactive coding
agent that can read, write, edit, search, and execute shell commands.`,
	PersistentPreRunE: configureLogging,
	RunE:              runRoot,
}

func setupViper(v *viper.Viper) {
	v.SetEnvPrefix("WORKSHOP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
}

func loadViperConfig(v *viper.Viper) error {
	return loadViperConfigWithPath(v, xdg.ConfigHome)
}

func loadViperConfigWithPath(v *viper.Viper, configHome string) error {
	configDir := filepath.Join(configHome, "workshop")
	v.AddConfigPath(configDir)
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}
	return nil
}

// defaultStoreDir returns the default path for persistent thread storage.
func defaultStoreDir() string {
	return filepath.Join(xdg.DataHome, "workshop", "threads")
}

func logLevel() (slog.Level, error) {
	levelStr := viper.GetString("log-level")
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelStr)); err != nil {
		return level, fmt.Errorf("invalid log level %q: %w", levelStr, err)
	}
	return level, nil
}

func configureLogging(cmd *cobra.Command, args []string) error {
	level, err := logLevel()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return nil
}

// defaultThinkingLevelForKind returns the per-kind default thinking
// level, applied only when the user has not configured one. Anthropic
// defaults to "medium" because every supported model benefits from
// extended thinking on hard turns. OpenAI-compatible providers keep
// "off" as the default, matching historical behavior. Any unknown or
// future kind falls back to "off" until a default is added here.
func defaultThinkingLevelForKind(kind string) string {
	if kind == "anthropic" {
		return "medium"
	}
	return "off"
}

// resolveThinkingLevelForConfig is the single source of truth for the
// "what thinking level should workshop use?" policy. When the user has
// not set a level (raw is the empty flag default), the per-kind default
// is substituted. Explicit user values — including "off" for Anthropic
// — are returned verbatim: a user who has chosen "off" in their config
// must keep getting "off", not get silently upgraded to "medium".
func resolveThinkingLevelForConfig(kind, raw string) string {
	if raw != "" {
		return raw
	}
	return defaultThinkingLevelForKind(kind)
}

func makeProviderConfig() app.ProviderConfig {
	kind := viper.GetString("provider.kind")
	return app.ProviderConfig{
		Kind:          kind,
		APIKey:        viper.GetString("provider.api-key"),
		Model:         viper.GetString("provider.model"),
		BaseURL:       viper.GetString("provider.base-url"),
		Temperature:   viper.GetFloat64("provider.temperature"),
		ThinkingLevel: resolveThinkingLevelForConfig(kind, viper.GetString("provider.thinking-level")),
		MaxTokens:     viper.GetInt64("provider.max-tokens"),
	}
}

func runRoot(cmd *cobra.Command, args []string) error {
	pc := makeProviderConfig()
	if pc.APIKey == "" {
		return fmt.Errorf("api key is required; set --provider.api-key or WORKSHOP_PROVIDER_API_KEY environment variable")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	maybeStartPProf(ctx, viper.GetBool("pprof"), viper.GetString("pprof.addr"))

	tracer, tracerShutdown, err := telemetry.NewTracer(viper.GetString("telemetry.traces.endpoint"))
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(shutdownCtx); err != nil {
			slog.Warn("tracer shutdown failed", "error", err)
		}
	}()

	meter, meterShutdown, err := telemetry.NewMeter(viper.GetString("telemetry.metrics.endpoint"))
	if err != nil {
		return fmt.Errorf("init metrics: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := meterShutdown(shutdownCtx); err != nil {
			slog.Warn("meter shutdown failed", "error", err)
		}
	}()

	cwd := ""
	if d, err := os.Getwd(); err == nil {
		cwd = d
	}

	opts := []app.Option{
		app.WithThreadID(viper.GetString("thread")),
		app.WithProvider(pc),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithWorkingDir(cwd),
		app.WithRole(viper.GetString("role")),
		app.WithTracer(tracer),
		app.WithMeter(meter),
		app.WithCompaction(app.CompactionConfig{
			MaxTokens: viper.GetInt("compaction.max-tokens"),
		}),
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		// Buffer slog output during the TUI to avoid corrupting the alternate
		// screen buffer. Flush after the TUI exits.
		buf := tui.NewLogBuffer()
		level, err := logLevel()
		if err != nil {
			return err
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
		uiErr := app.RunTUI(ctx, opts...)
		_ = buf.FlushTo(os.Stderr)
		return uiErr
	}
	return app.RunStdio(ctx, opts...)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
