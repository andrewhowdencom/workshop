package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
	// `provider` is the name of the inference (default) provider and
	// must reference a key in the `providers:` map. Per-named-provider
	// fields (api-key, model, base-url, etc.) are configured in
	// config.yaml or via the WORKSHOP_PROVIDER_<NAME>_<FIELD> env vars;
	// cobra flags don't fit dynamic names, so they're not exposed here.
	rootCmd.PersistentFlags().String("provider", "", "Name of the default inference provider (must be a key in the providers: section)")
	rootCmd.PersistentFlags().String("store.dir", "", "Directory for persistent JSON thread storage (default: $XDG_DATA_HOME/workshop/threads)")
	rootCmd.PersistentFlags().String("role", "", "Initial role for new threads")
	rootCmd.PersistentFlags().Bool("pprof", false, "Enable the pprof debug server")
	rootCmd.PersistentFlags().String("pprof.addr", defaultPProfAddr, "TCP address for the pprof server")
	rootCmd.PersistentFlags().String("telemetry.traces.endpoint", "", "OpenTelemetry OTLP/HTTP endpoint URL for traces (e.g. http://localhost:4318); empty = disabled")
	rootCmd.PersistentFlags().String("telemetry.metrics.endpoint", "", "OpenTelemetry OTLP/HTTP endpoint URL for metrics (e.g. http://localhost:4318); empty = disabled")
	rootCmd.PersistentFlags().Int("compaction.max-tokens", 100000, "Per-invocation output budget for /compact, forwarded to compaction.Summarize via models.Spec.MaxOutputTokens (0 = use the ore/compaction framework default, 8192)")

	rootCmd.Flags().String("thread", "", "Existing thread UUID to resume")

	setupViper(viper.GetViper())
	if err := loadViperConfig(viper.GetViper()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if err := bindNamedProviderEnvVars(viper.GetViper()); err != nil {
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

// namedProviderFields is the canonical list of fields that
// loadProvidersConfig reads for each defined named provider, and that
// bindNamedProviderEnvVars binds an env var for. Order is not
// significant; the list exists so the two functions cannot drift
// apart.
var namedProviderFields = []string{
	"kind",
	"api-key",
	"model",
	"base-url",
	"temperature",
	"thinking-level",
	"max-tokens",
}

// bindNamedProviderEnvVars discovers the named providers from the
// already-loaded config file and binds each per-name field to a
// per-name env var (WORKSHOP_PROVIDER_<UPPER_NAME>_<FIELD>). This
// must run AFTER loadViperConfig (so the names are known) and BEFORE
// viper.BindPFlags (so the flag takes precedence over env, per viper's
// standard priority: explicit Set > flag > env > config > default).
//
// The mapping uses the singular WORKSHOP_PROVIDER_ prefix (matching
// the user's mental model of "a provider config") rather than
// WORKSHOP_PROVIDERS_ (which is what the default dot-to-underscore
// replacer would produce from the viper key `providers.<name>.<field>`).
//
// Env-var-only configurations (no config file, names supplied via
// env) are not supported by design: the names must be declared in
// the config file so the loader can discover them.
func bindNamedProviderEnvVars(v *viper.Viper) error {
	raw := v.GetStringMap("providers")
	for name := range raw {
		if name == "" {
			continue
		}
		for _, field := range namedProviderFields {
			envKey := "WORKSHOP_PROVIDER_" + strings.ToUpper(name) + "_" + strings.ToUpper(strings.ReplaceAll(field, "-", "_"))
			if err := v.BindEnv("providers."+name+"."+field, envKey); err != nil {
				return fmt.Errorf("bind env %s for providers.%s.%s: %w", envKey, name, field, err)
			}
		}
	}
	return nil
}

// loadProvidersConfig reads the named-providers shape from viper and
// returns (defaultName, providers, nil) on success or ("", nil, err)
// on any validation failure. Each named provider's ThinkingLevel is
// run through resolveThinkingLevelForConfig so the per-kind default
// (medium for anthropic, off for openai) is applied when the user has
// not configured one. The env-var binding pass must have run first so
// any WORKSHOP_PROVIDER_<NAME>_<FIELD> env vars are visible to the
// per-leaf viper reads.
func loadProvidersConfig(v *viper.Viper) (string, map[string]app.ProviderConfig, error) {
	defaultName := v.GetString("provider")
	if defaultName == "" {
		return "", nil, fmt.Errorf("provider: <name> is required; set the default inference provider in config.yaml or via --provider")
	}

	raw := v.GetStringMap("providers")
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("no providers defined; set the providers: section in config.yaml")
	}

	providers := make(map[string]app.ProviderConfig, len(raw))
	for name := range raw {
		kind := v.GetString("providers." + name + ".kind")
		pc := app.ProviderConfig{
			Kind:          kind,
			APIKey:        v.GetString("providers." + name + ".api-key"),
			Model:         v.GetString("providers." + name + ".model"),
			BaseURL:       v.GetString("providers." + name + ".base-url"),
			Temperature:   v.GetFloat64("providers." + name + ".temperature"),
			ThinkingLevel: resolveThinkingLevelForConfig(kind, v.GetString("providers."+name+".thinking-level")),
			MaxTokens:     v.GetInt64("providers." + name + ".max-tokens"),
		}
		providers[name] = pc
	}

	if _, ok := providers[defaultName]; !ok {
		return "", nil, fmt.Errorf("default provider %q is not defined in providers: section (defined: %s)", defaultName, definedProviderNames(providers))
	}

	return defaultName, providers, nil
}

// definedProviderNames returns the keys of the providers map in a
// stable, sorted order for use in error messages.
func definedProviderNames(m map[string]app.ProviderConfig) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func runRoot(cmd *cobra.Command, args []string) error {
	defaultName, providers, err := loadProvidersConfig(viper.GetViper())
	if err != nil {
		return err
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
		app.WithDefaultProviderName(defaultName),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithWorkingDir(cwd),
		app.WithRole(viper.GetString("role")),
		app.WithTracer(tracer),
		app.WithMeter(meter),
		app.WithCompaction(app.CompactionConfig{
			Provider:  viper.GetString("compaction.provider"),
			MaxTokens: viper.GetInt("compaction.max-tokens"),
		}),
	}
	// Register every defined named provider. Order doesn't matter
	// because the consumers (inference, compaction) look up by name.
	for name, pc := range providers {
		opts = append(opts, app.WithProvider(name, pc))
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
