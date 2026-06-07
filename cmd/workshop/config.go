package main

import (
	"fmt"
	"os"

	"github.com/adrg/xdg"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage workshop configuration",
}

var configInitCmd = &cobra.Command{
	Use:     "init",
	Aliases: []string{"initialize"},
	Short:   "Initialize a configuration file with current settings",
	RunE:    runConfigInit,
}

func init() {
	configCmd.AddCommand(configInitCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	configPath, err := xdg.ConfigFile("workshop/config.yaml")
	if err != nil {
		return fmt.Errorf("resolve config file path: %w", err)
	}
	return runConfigInitWithPath(cmd, args, configPath)
}

func runConfigInitWithPath(cmd *cobra.Command, args []string, configPath string) error {
	settings := buildConfigMap()

	data, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal config to YAML: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	fmt.Printf("Configuration written to %s\n", configPath)
	return nil
}

func buildConfigMap() map[string]interface{} {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}

	return map[string]interface{}{
		"log-level": viper.GetString("log-level"),
		"provider": map[string]interface{}{
			"kind":             viper.GetString("provider.kind"),
			"api-key":          viper.GetString("provider.api-key"),
			"model":            viper.GetString("provider.model"),
			"base-url":         viper.GetString("provider.base-url"),
			"temperature":      viper.GetFloat64("provider.temperature"),
			"reasoning-effort": viper.GetString("provider.reasoning-effort"),
		},
		"store": map[string]interface{}{
			"dir": storeDir,
		},
		"http": map[string]interface{}{
			"addr": viper.GetString("http.addr"),
		},
		"pprof":      viper.GetBool("pprof"),
		"pprof.addr": viper.GetString("pprof.addr"),
		"compaction": map[string]interface{}{
			"max-tokens": viper.GetInt("compaction.max-tokens"),
		},
		"tracing": map[string]interface{}{
			"endpoint": viper.GetString("tracing.endpoint"),
		},
	}
}
