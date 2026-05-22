package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestSetupViper(t *testing.T) {
	v := viper.New()
	setupViper(v)

	// Verify the env prefix and replacer are wired by setting an env var.
	t.Setenv("WORKSHOP_MODEL", "env-model")
	if got := v.GetString("model"); got != "env-model" {
		t.Errorf("setupViper: model from env = %q, want %q", got, "env-model")
	}
}

func TestLoadViperConfig_MissingFile(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("missing config file should not error, got: %v", err)
	}

	if got := v.GetString("model"); got != "" {
		t.Errorf("missing config: model = %q, want empty", got)
	}
}

func TestLoadViperConfig_ValidYAML(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "workshop")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("model: gpt-4o-mini\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("valid config should not error, got: %v", err)
	}

	if got := v.GetString("model"); got != "gpt-4o-mini" {
		t.Errorf("valid config: model = %q, want %q", got, "gpt-4o-mini")
	}
}

func TestLoadViperConfig_MalformedYAML(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "workshop")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("not: yaml: :: bad\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err == nil {
		t.Fatal("malformed config should error, got nil")
	}
}

func TestPrecedence_ConfigFileThenDefault(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "workshop")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("model: file-model\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("valid config should not error, got: %v", err)
	}

	if got := v.GetString("model"); got != "file-model" {
		t.Errorf("config file value: model = %q, want %q", got, "file-model")
	}

	// Simulate a flag override via Set.
	v.Set("model", "flag-model")
	if got := v.GetString("model"); got != "flag-model" {
		t.Errorf("flag override: model = %q, want %q", got, "flag-model")
	}
}
