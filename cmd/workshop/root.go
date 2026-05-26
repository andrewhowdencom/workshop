package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/andrewhowdencom/workshop/internal/app"
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
	rootCmd.PersistentFlags().String("provider.reasoning-effort", "", "Reasoning effort for the provider (low, medium, high)")
	rootCmd.PersistentFlags().String("store.dir", "", "Directory for persistent JSON thread storage")

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

func configureLogging(cmd *cobra.Command, args []string) error {
	levelStr := viper.GetString("log-level")
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelStr)); err != nil {
		return fmt.Errorf("invalid log level %q: %w", levelStr, err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return nil
}

func runRoot(cmd *cobra.Command, args []string) error {
	apiKey := viper.GetString("provider.api-key")
	if apiKey == "" {
		return fmt.Errorf("api key is required; set --provider.api-key or WORKSHOP_PROVIDER_API_KEY environment variable")
	}

	pc := app.ProviderConfig{
		Kind:            viper.GetString("provider.kind"),
		APIKey:          apiKey,
		Model:           viper.GetString("provider.model"),
		BaseURL:         viper.GetString("provider.base-url"),
		Temperature:     viper.GetFloat64("provider.temperature"),
		ReasoningEffort: viper.GetString("provider.reasoning-effort"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cwd := ""
	if d, err := os.Getwd(); err == nil {
		cwd = d
	}

	opts := []app.Option{
		app.WithThreadID(viper.GetString("thread")),
		app.WithProvider(pc),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithWorkingDir(cwd),
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		return app.RunTUI(ctx, opts...)
	}
	return app.RunStdio(ctx, opts...)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
