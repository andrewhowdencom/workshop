package main

import (
	"fmt"
	"os"
	"sort"

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

	// Resolve the named-providers shape from viper. Each defined name
	// is emitted as a sub-map under `providers:` with the per-name
	// fields. The default inference name is emitted as a top-level
	// `provider:` string. Names are sorted so the emitted YAML is
	// stable across runs.
	rawProviders := viper.GetStringMap("providers")
	providerNames := make([]string, 0, len(rawProviders))
	for name := range rawProviders {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	providers := make(map[string]interface{}, len(providerNames))
	for _, name := range providerNames {
		kind := viper.GetString("providers." + name + ".kind")
		providers[name] = map[string]interface{}{
			"kind":           kind,
			"api-key":        viper.GetString("providers." + name + ".api-key"),
			"model":          viper.GetString("providers." + name + ".model"),
			"base-url":       viper.GetString("providers." + name + ".base-url"),
			"temperature":    viper.GetFloat64("providers." + name + ".temperature"),
			"thinking-level": resolveThinkingLevelForConfig(kind, viper.GetString("providers."+name+".thinking-level")),
			"max-tokens":     viper.GetInt64("providers." + name + ".max-tokens"),
		}
	}

	return map[string]interface{}{
		"log-level": viper.GetString("log-level"),
		"provider":  viper.GetString("provider"),
		"providers": providers,
		"store": map[string]interface{}{
			"dir": storeDir,
		},
		"http": map[string]interface{}{
			"addr": viper.GetString("http.addr"),
		},
		"pprof":      viper.GetBool("pprof"),
		"pprof.addr": viper.GetString("pprof.addr"),
		"compaction": map[string]interface{}{
			"provider":   viper.GetString("compaction.provider"),
			"max-tokens": viper.GetInt("compaction.max-tokens"),
		},
		"telemetry": map[string]interface{}{
			"traces": map[string]interface{}{
				"endpoint": viper.GetString("telemetry.traces.endpoint"),
			},
			"metrics": map[string]interface{}{
				"endpoint": viper.GetString("telemetry.metrics.endpoint"),
			},
		},
	}
}
