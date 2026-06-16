package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

func setViperValue(t *testing.T, key, value string) {
	old := viper.GetString(key)
	viper.Set(key, value)
	t.Cleanup(func() { viper.Set(key, old) })
}

// setViperInt64Value mirrors setViperValue for int64 keys, restoring the
// previous value on test cleanup. Used for the provider.max-tokens flag.
func setViperInt64Value(t *testing.T, key string, value int64) {
	old := viper.GetInt64(key)
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
	setViperValue(t, "provider", "haiku")
	setViperValue(t, "providers.haiku.kind", "openai")
	setViperValue(t, "providers.haiku.api-key", "test-key")
	setViperValue(t, "providers.haiku.model", "gpt-4o")
	setViperValue(t, "providers.haiku.base-url", "http://test")
	setViperValue(t, "providers.haiku.temperature", "0.7")
	setViperValue(t, "providers.haiku.thinking-level", "medium")
	setViperInt64Value(t, "providers.haiku.max-tokens", 32000)
	setViperValue(t, "compaction.provider", "haiku")
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

	// The top-level `provider:` is a string name reference.
	if got, want := settings["provider"], "haiku"; got != want {
		t.Errorf("provider = %v, want %v", got, want)
	}

	providers, ok := settings["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("providers section missing or not a map")
	}
	prov, ok := providers["haiku"].(map[string]interface{})
	if !ok {
		t.Fatal("providers.haiku section missing or not a map")
	}
	if got, want := prov["kind"], "openai"; got != want {
		t.Errorf("providers.haiku.kind = %v, want %v", got, want)
	}
	if got, want := prov["api-key"], "test-key"; got != want {
		t.Errorf("providers.haiku.api-key = %v, want %v", got, want)
	}
	if got, want := prov["model"], "gpt-4o"; got != want {
		t.Errorf("providers.haiku.model = %v, want %v", got, want)
	}
	if got, want := prov["base-url"], "http://test"; got != want {
		t.Errorf("providers.haiku.base-url = %v, want %v", got, want)
	}
	if got, want := prov["temperature"], 0.7; got != want {
		t.Errorf("providers.haiku.temperature = %v, want %v", got, want)
	}
	if got, want := prov["thinking-level"], "medium"; got != want {
		t.Errorf("providers.haiku.thinking-level = %v, want %v", got, want)
	}
	// YAML round-trips int64 as int; compare as int (values fit comfortably).
	if got, want := prov["max-tokens"], 32000; got != want {
		t.Errorf("providers.haiku.max-tokens = %v (%T), want %v (%T)", got, got, want, want)
	}

	compaction, ok := settings["compaction"].(map[string]interface{})
	if !ok {
		t.Fatal("compaction section missing or not a map")
	}
	if got, want := compaction["max-tokens"], 100000; got != want {
		t.Errorf("compaction.max-tokens = %v, want %v", got, want)
	}
	if got, want := compaction["provider"], "haiku"; got != want {
		t.Errorf("compaction.provider = %v, want %v", got, want)
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

func TestRunConfigInitWithPath_UsesDefaultStoreDir(t *testing.T) {
	setViperValue(t, "provider", "haiku")
	setViperValue(t, "providers.haiku.model", "gpt-4o")
	setViperValue(t, "store.dir", "")

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

	store, ok := settings["store"].(map[string]interface{})
	if !ok {
		t.Fatal("store section missing or not a map")
	}
	got, ok := store["dir"].(string)
	if !ok {
		t.Fatalf("store.dir is not a string: %T", store["dir"])
	}
	if got == "" {
		t.Errorf("store.dir = empty, want non-empty default")
	}
	if !strings.Contains(got, filepath.Join("workshop", "threads")) {
		t.Errorf("store.dir = %q, want to contain 'workshop/threads'", got)
	}
}

func TestRunConfigInitWithPath_FilePermissions(t *testing.T) {
	setViperValue(t, "provider", "haiku")
	setViperValue(t, "providers.haiku.model", "gpt-4o")

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
	setViperValue(t, "provider", "haiku")
	setViperValue(t, "providers.haiku.model", "first-model")
	if err := runConfigInitWithPath(nil, nil, tmpFile); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Second write with different value.
	setViperValue(t, "providers.haiku.model", "second-model")
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

	providers, ok := settings["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("providers section missing or not a map")
	}
	prov, ok := providers["haiku"].(map[string]interface{})
	if !ok {
		t.Fatal("providers.haiku section missing or not a map")
	}
	if got, want := prov["model"], "second-model"; got != want {
		t.Errorf("providers.haiku.model after overwrite = %v, want %v", got, want)
	}
}

// TestBuildConfigMap_AppliesAnthropicDefault verifies that the emitted
// YAML from `workshop config init` reflects the per-kind default for
// each named provider: an anthropic user who has not configured
// thinking-level gets "medium" in the written config, so the round-trip
// is honest about what workshop will do at runtime. An openai user
// keeps "off". A user who has explicitly set "off" for anthropic
// keeps "off" (no silent upgrade).
func TestBuildConfigMap_AppliesAnthropicDefault(t *testing.T) {
	t.Run("anthropic empty -> medium", func(t *testing.T) {
		setViperValue(t, "provider", "haiku")
		setViperValue(t, "providers.haiku.kind", "anthropic")
		setViperValue(t, "providers.haiku.thinking-level", "")

		settings := buildConfigMap()
		prov := assertProviderMap(t, settings, "haiku")
		if got, want := prov["thinking-level"], "medium"; got != want {
			t.Errorf("providers.haiku.thinking-level = %v, want %v", got, want)
		}
	})

	t.Run("openai empty -> off", func(t *testing.T) {
		setViperValue(t, "provider", "haiku")
		setViperValue(t, "providers.haiku.kind", "openai")
		setViperValue(t, "providers.haiku.thinking-level", "")

		settings := buildConfigMap()
		prov := assertProviderMap(t, settings, "haiku")
		if got, want := prov["thinking-level"], "off"; got != want {
			t.Errorf("providers.haiku.thinking-level = %v, want %v", got, want)
		}
	})

	t.Run("anthropic explicit off -> off (no silent upgrade)", func(t *testing.T) {
		setViperValue(t, "provider", "haiku")
		setViperValue(t, "providers.haiku.kind", "anthropic")
		setViperValue(t, "providers.haiku.thinking-level", "off")

		settings := buildConfigMap()
		prov := assertProviderMap(t, settings, "haiku")
		if got, want := prov["thinking-level"], "off"; got != want {
			t.Errorf("providers.haiku.thinking-level = %v, want %v (explicit user value must be preserved)", got, want)
		}
	})
}

// assertProviderMap is a small helper that pulls the sub-map for a
// given named provider out of the buildConfigMap output. It fails
// the test (via t.Fatal) if the shape is wrong, so callers can write
// assertion-only code.
func assertProviderMap(t *testing.T, settings map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	providers, ok := settings["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("providers section missing or not a map")
	}
	prov, ok := providers[name].(map[string]interface{})
	if !ok {
		t.Fatalf("providers.%s section missing or not a map", name)
	}
	return prov
}
