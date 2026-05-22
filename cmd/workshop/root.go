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
)

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

func init() {
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")

	rootCmd.Flags().String("thread", "", "Existing thread UUID to resume")
	rootCmd.Flags().String("api.key", "", "OpenAI-compatible API key")
	rootCmd.Flags().String("model", "gpt-4o", "Model name (e.g. gpt-4o)")
	rootCmd.Flags().String("base.url", "", "Custom API base URL")
	rootCmd.Flags().String("store.dir", "", "Directory for persistent JSON thread storage")

	setupViper(viper.GetViper())
	if err := loadViperConfig(viper.GetViper()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	cobra.CheckErr(viper.BindPFlags(rootCmd.PersistentFlags()))
	cobra.CheckErr(viper.BindPFlags(rootCmd.Flags()))
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
	apiKey := viper.GetString("api.key")
	if apiKey == "" {
		return fmt.Errorf("api key is required; set --api.key or WORKSHOP_API_KEY environment variable")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return app.Run(ctx,
		app.WithThreadID(viper.GetString("thread")),
		app.WithAPIKey(apiKey),
		app.WithModel(viper.GetString("model")),
		app.WithBaseURL(viper.GetString("base.url")),
		app.WithStoreDir(viper.GetString("store.dir")),
	)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
