package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewhowdencom/workshop/internal/app"
	"github.com/spf13/viper"
)

// newTestViper returns a fresh viper instance with the env prefix and
// replacer set up the same way the production runtime does. Each test
// gets its own instance so state set with `v.Set` cannot leak into
// the next test.
func newTestViper() *viper.Viper {
	v := viper.New()
	setupViper(v)
	return v
}

func TestLoadProvidersConfig_Valid(t *testing.T) {
	v := newTestViper()
	v.Set("provider", "haiku")
	v.Set("providers.haiku.kind", "openai")
	v.Set("providers.haiku.api-key", "sk-test")
	v.Set("providers.haiku.model", "gpt-4o")
	v.Set("providers.haiku.base-url", "http://test")
	v.Set("providers.haiku.temperature", 0.7)
	v.Set("providers.haiku.thinking-level", "medium")
	v.Set("providers.haiku.max-tokens", int64(32000))

	defaultName, providers, err := loadProvidersConfig(v)
	if err != nil {
		t.Fatalf("loadProvidersConfig: %v", err)
	}
	if defaultName != "haiku" {
		t.Errorf("defaultName = %q, want haiku", defaultName)
	}
	pc, ok := providers["haiku"]
	if !ok {
		t.Fatal("providers[haiku] missing")
	}
	if pc.Kind != "openai" {
		t.Errorf("Kind = %q, want openai", pc.Kind)
	}
	if pc.APIKey != "sk-test" {
		t.Errorf("APIKey = %q, want sk-test", pc.APIKey)
	}
	if pc.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", pc.Model)
	}
	if pc.BaseURL != "http://test" {
		t.Errorf("BaseURL = %q, want http://test", pc.BaseURL)
	}
	if pc.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", pc.Temperature)
	}
	if pc.ThinkingLevel != "medium" {
		t.Errorf("ThinkingLevel = %q, want medium", pc.ThinkingLevel)
	}
	if pc.MaxTokens != 32000 {
		t.Errorf("MaxTokens = %d, want 32000", pc.MaxTokens)
	}
}

func TestLoadProvidersConfig_MultipleProviders(t *testing.T) {
	v := newTestViper()
	v.Set("provider", "sonnet")
	v.Set("providers.haiku.kind", "anthropic")
	v.Set("providers.haiku.api-key", "sk-ant-haiku")
	v.Set("providers.haiku.model", "claude-haiku")
	v.Set("providers.sonnet.kind", "anthropic")
	v.Set("providers.sonnet.api-key", "sk-ant-sonnet")
	v.Set("providers.sonnet.model", "claude-sonnet-4-5")

	defaultName, providers, err := loadProvidersConfig(v)
	if err != nil {
		t.Fatalf("loadProvidersConfig: %v", err)
	}
	if defaultName != "sonnet" {
		t.Errorf("defaultName = %q, want sonnet", defaultName)
	}
	if len(providers) != 2 {
		t.Errorf("len(providers) = %d, want 2", len(providers))
	}
	if providers["haiku"].APIKey != "sk-ant-haiku" {
		t.Errorf("haiku APIKey = %q, want sk-ant-haiku", providers["haiku"].APIKey)
	}
	if providers["sonnet"].APIKey != "sk-ant-sonnet" {
		t.Errorf("sonnet APIKey = %q, want sk-ant-sonnet", providers["sonnet"].APIKey)
	}
}

func TestLoadProvidersConfig_MissingDefaultName(t *testing.T) {
	v := newTestViper()
	v.Set("providers.haiku.kind", "openai")
	v.Set("providers.haiku.api-key", "sk-test")
	v.Set("providers.haiku.model", "gpt-4o")

	_, _, err := loadProvidersConfig(v)
	if err == nil {
		t.Fatal("expected error for missing provider: <name>")
	}
	if !strings.Contains(err.Error(), "provider: <name> is required") {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestLoadProvidersConfig_NoProviders(t *testing.T) {
	v := newTestViper()
	v.Set("provider", "haiku")

	_, _, err := loadProvidersConfig(v)
	if err == nil {
		t.Fatal("expected error for empty providers: section")
	}
	if !strings.Contains(err.Error(), "no providers defined") {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestLoadProvidersConfig_DefaultReferencesUndefined(t *testing.T) {
	v := newTestViper()
	v.Set("provider", "nonexistent")
	v.Set("providers.haiku.kind", "openai")
	v.Set("providers.haiku.api-key", "sk-test")
	v.Set("providers.haiku.model", "gpt-4o")

	_, _, err := loadProvidersConfig(v)
	if err == nil {
		t.Fatal("expected error for undefined default provider")
	}
	if !strings.Contains(err.Error(), `default provider "nonexistent" is not defined`) {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestLoadProvidersConfig_AnthropicThinkingLevelDefault(t *testing.T) {
	// An anthropic named provider with no thinking-level configured
	// gets the per-kind default ("medium"). An openai provider gets
	// the historical "off". An explicit "off" on anthropic is
	// preserved (no silent upgrade).
	cases := []struct {
		name             string
		providerName     string
		kind             string
		rawThinkingLevel string
		wantThinking     string
	}{
		{"anthropic empty -> medium", "haiku", "anthropic", "", "medium"},
		{"openai empty -> off", "haiku", "openai", "", "off"},
		{"anthropic explicit off -> off", "haiku", "anthropic", "off", "off"},
		{"anthropic explicit medium -> medium", "haiku", "anthropic", "medium", "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestViper()
			v.Set("provider", tc.providerName)
			v.Set("providers."+tc.providerName+".kind", tc.kind)
			v.Set("providers."+tc.providerName+".api-key", "sk-test")
			v.Set("providers."+tc.providerName+".model", "test-model")
			v.Set("providers."+tc.providerName+".thinking-level", tc.rawThinkingLevel)

			_, providers, err := loadProvidersConfig(v)
			if err != nil {
				t.Fatalf("loadProvidersConfig: %v", err)
			}
			got := providers[tc.providerName].ThinkingLevel
			if got != tc.wantThinking {
				t.Errorf("ThinkingLevel = %q, want %q", got, tc.wantThinking)
			}
		})
	}
}

func TestBindNamedProviderEnvVars_BindsPerNameFields(t *testing.T) {
	// NOTE: this test deliberately does NOT call v.Set on api-key; the
	// env var is the only source of the value, so we can prove the
	// env binding reaches viper.GetString.
	v := newTestViper()
	v.Set("providers.haiku.kind", "openai")

	if err := bindNamedProviderEnvVars(v); err != nil {
		t.Fatalf("bindNamedProviderEnvVars: %v", err)
	}

	t.Setenv("WORKSHOP_PROVIDER_HAIKU_API_KEY", "from-env")
	if got := v.GetString("providers.haiku.api-key"); got != "from-env" {
		t.Errorf("api-key from env = %q, want from-env", got)
	}
}

func TestBindNamedProviderEnvVars_NoNamesNoOp(t *testing.T) {
	v := newTestViper()
	// No providers: the function should be a no-op (not an error).
	if err := bindNamedProviderEnvVars(v); err != nil {
		t.Errorf("bindNamedProviderEnvVars: %v", err)
	}
}

func TestSetupViper(t *testing.T) {
	v := viper.New()
	setupViper(v)

	// Verify the env prefix and replacer are wired by setting a
	// non-provider env var (the WORKSHOP_PROVIDER_<NAME>_<FIELD> pattern
	// is bound explicitly via bindNamedProviderEnvVars, not by the
	// default replacer).
	t.Setenv("WORKSHOP_LOG_LEVEL", "debug")
	if got := v.GetString("log-level"); got != "debug" {
		t.Errorf("setupViper: log-level from env = %q, want debug", got)
	}
}

// definedProviderNames is exported from root.go for use in error
// messages; cover it lightly so a refactor of the sorting/joining
// is forced through a test.
func TestDefinedProviderNames_Sorted(t *testing.T) {
	got := definedProviderNames(map[string]app.ProviderConfig{
		"zeta":  {},
		"alpha": {},
		"mu":    {},
	})
	if got != "alpha, mu, zeta" {
		t.Errorf("definedProviderNames = %q, want %q", got, "alpha, mu, zeta")
	}
}

func TestLoadViperConfig_MissingFile(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("missing config file should not error, got: %v", err)
	}

	if got := v.GetString("provider.model"); got != "" {
		t.Errorf("missing config: provider.model = %q, want empty", got)
	}
}

func TestLoadViperConfig_ValidYAML(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "workshop")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("provider:\n  model: gpt-4o-mini\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("valid config should not error, got: %v", err)
	}

	if got := v.GetString("provider.model"); got != "gpt-4o-mini" {
		t.Errorf("valid config: provider.model = %q, want %q", got, "gpt-4o-mini")
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
	content := []byte("provider:\n  model: file-model\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("valid config should not error, got: %v", err)
	}

	if got := v.GetString("provider.model"); got != "file-model" {
		t.Errorf("config file value: provider.model = %q, want %q", got, "file-model")
	}

	// Simulate a flag override via Set.
	v.Set("provider.model", "flag-model")
	if got := v.GetString("provider.model"); got != "flag-model" {
		t.Errorf("flag override: provider.model = %q, want %q", got, "flag-model")
	}
}

func TestRoleFlag_ConfigFile(t *testing.T) {
	v := viper.New()
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "workshop")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("role: planner\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := loadViperConfigWithPath(v, tmpDir); err != nil {
		t.Fatalf("valid config should not error, got: %v", err)
	}

	if got := v.GetString("role"); got != "planner" {
		t.Errorf("config file value: role = %q, want %q", got, "planner")
	}
}

func TestRoleFlag_Environment(t *testing.T) {
	v := viper.New()
	setupViper(v)

	t.Setenv("WORKSHOP_ROLE", "reviewer")
	if got := v.GetString("role"); got != "reviewer" {
		t.Errorf("env value: role = %q, want %q", got, "reviewer")
	}
}

func TestDefaultStoreDir(t *testing.T) {
	got := defaultStoreDir()
	if !strings.Contains(got, filepath.Join("workshop", "threads")) {
		t.Errorf("defaultStoreDir = %q, want to contain 'workshop/threads'", got)
	}
}

// TestDefaultThinkingLevelForKind locks in the per-kind defaults that
// drive the resolver. Anthropic gets "medium" because every supported
// model benefits from extended thinking on hard turns; everything else
// stays at "off" until a new kind is added here.
func TestDefaultThinkingLevelForKind(t *testing.T) {
	cases := []struct {
		name string
		kind string
		want string
	}{
		{"anthropic", "anthropic", "medium"},
		{"openai", "openai", "off"},
		{"empty kind", "", "off"},
		{"unknown kind", "future-kind", "off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultThinkingLevelForKind(tc.kind); got != tc.want {
				t.Errorf("defaultThinkingLevelForKind(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestResolveThinkingLevelForConfig locks in the two halves of the
// resolver's contract: (1) non-empty raw values pass through verbatim,
// including the "off" pair with the "anthropic" kind — this is the
// "do not silently upgrade" guarantee; (2) the empty sentinel
// resolves to the per-kind default. A future change that re-introduces
// the silent upgrade will fail on the first case below.
func TestResolveThinkingLevelForConfig(t *testing.T) {
	cases := []struct {
		name string
		kind string
		raw  string
		want string
	}{
		// Pass-through: explicit user values are preserved verbatim.
		{"explicit off for anthropic", "anthropic", "off", "off"},
		{"explicit medium for anthropic", "anthropic", "medium", "medium"},
		{"explicit high for openai", "openai", "high", "high"},
		{"explicit max for empty kind", "", "max", "max"},
		{"bogus value passes through verbatim", "anthropic", "bogus", "bogus"},

		// Default substitution: empty raw falls back to the per-kind default.
		{"anthropic empty -> medium", "anthropic", "", "medium"},
		{"openai empty -> off", "openai", "", "off"},
		{"empty kind empty -> off", "", "", "off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveThinkingLevelForConfig(tc.kind, tc.raw); got != tc.want {
				t.Errorf("resolveThinkingLevelForConfig(%q, %q) = %q, want %q", tc.kind, tc.raw, got, tc.want)
			}
		})
	}
}

// (The TestMakeProviderConfig_* tests were removed in the
// refactor-config-to-named-providers change. The new
// TestLoadProvidersConfig_AnthropicThinkingLevelDefault covers the
// per-kind defaulting contract under the named-providers shape.)
