package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

func setViperValue(t *testing.T, key, value string) {
	old := viper.GetString(key)
	viper.Set(key, value)
	t.Cleanup(func() { viper.Set(key, old) })
}

func TestBuildConfigMap_ExcludesThread(t *testing.T) {
	setViperValue(t, "thread", "uuid-123")
	settings := buildConfigMap()

	if _, ok := settings["thread"]; ok {
		t.Error("buildConfigMap should not include thread key")
	}
}

func TestRunConfigInitWithPath_WritesCorrectYAML(t *testing.T) {
	setViperValue(t, "log-level", "debug")
	setViperValue(t, "provider.kind", "openai")
	setViperValue(t, "provider.api-key", "test-key")
	setViperValue(t, "provider.model", "gpt-4o")
	setViperValue(t, "provider.base-url", "http://test")
	setViperValue(t, "store.dir", "/tmp/store")

	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := runConfigInitWithPath(nil, nil, tmpFile); err != nil {
		t.Fatalf("runConfigInitWithPath: %v", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var settings map[string]interface{}
	if err := yaml.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}

	if got, want := settings["log-level"], "debug"; got != want {
		t.Errorf("log-level = %v, want %v", got, want)
	}

	prov, ok := settings["provider"].(map[string]interface{})
	if !ok {
		t.Fatal("provider section missing or not a map")
	}
	if got, want := prov["kind"], "openai"; got != want {
		t.Errorf("provider.kind = %v, want %v", got, want)
	}
	if got, want := prov["api-key"], "test-key"; got != want {
		t.Errorf("provider.api-key = %v, want %v", got, want)
	}
	if got, want := prov["model"], "gpt-4o"; got != want {
		t.Errorf("provider.model = %v, want %v", got, want)
	}
	if got, want := prov["base-url"], "http://test"; got != want {
		t.Errorf("provider.base-url = %v, want %v", got, want)
	}

	store, ok := settings["store"].(map[string]interface{})
	if !ok {
		t.Fatal("store section missing or not a map")
	}
	if got, want := store["dir"], "/tmp/store"; got != want {
		t.Errorf("store.dir = %v, want %v", got, want)
	}

	http, ok := settings["http"].(map[string]interface{})
	if !ok {
		t.Fatal("http section missing or not a map")
	}
	if got, want := http["addr"], ":8080"; got != want {
		t.Errorf("http.addr = %v, want %v", got, want)
	}
}

func TestRunConfigInitWithPath_FilePermissions(t *testing.T) {
	setViperValue(t, "provider.model", "gpt-4o")

	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := runConfigInitWithPath(nil, nil, tmpFile); err != nil {
		t.Fatalf("runConfigInitWithPath: %v", err)
	}

	info, err := os.Stat(tmpFile)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %04o, want %04o", perm, 0o600)
	}
}

func TestRunConfigInitWithPath_OverwritesExisting(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")

	// First write.
	setViperValue(t, "provider.model", "first-model")
	if err := runConfigInitWithPath(nil, nil, tmpFile); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Second write with different value.
	setViperValue(t, "provider.model", "second-model")
	if err := runConfigInitWithPath(nil, nil, tmpFile); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var settings map[string]interface{}
	if err := yaml.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}

	prov, ok := settings["provider"].(map[string]interface{})
	if !ok {
		t.Fatal("provider section missing or not a map")
	}
	if got, want := prov["model"], "second-model"; got != want {
		t.Errorf("provider.model after overwrite = %v, want %v", got, want)
	}
}
