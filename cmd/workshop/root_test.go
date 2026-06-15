package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestMakeProviderConfig(t *testing.T) {
	viper.Set("provider.kind", "openai")
	viper.Set("provider.api-key", "sk-test")
	viper.Set("provider.model", "gpt-4o")
	viper.Set("provider.base-url", "http://test")
	viper.Set("provider.temperature", 0.7)
	viper.Set("provider.thinking-level", "medium")
	viper.Set("provider.max-tokens", int64(32000))

	t.Cleanup(func() {
		viper.Set("provider.kind", "openai")
		viper.Set("provider.api-key", "")
		viper.Set("provider.model", "gpt-4o")
		viper.Set("provider.base-url", "")
		viper.Set("provider.temperature", 0)
		viper.Set("provider.thinking-level", "off")
		viper.Set("provider.max-tokens", int64(0))
	})

	pc := makeProviderConfig()

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

func TestSetupViper(t *testing.T) {
	v := viper.New()
	setupViper(v)

	// Verify the env prefix and replacer are wired by setting an env var.
	t.Setenv("WORKSHOP_PROVIDER_MODEL", "env-model")
	if got := v.GetString("provider.model"); got != "env-model" {
		t.Errorf("setupViper: provider.model from env = %q, want %q", got, "env-model")
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

// TestMakeProviderConfig_AnthropicDefault verifies the end-to-end
// defaulting: a user who selects the anthropic kind and does not
// configure --provider.thinking-level gets "medium" at runtime.
func TestMakeProviderConfig_AnthropicDefault(t *testing.T) {
	viper.Set("provider.kind", "anthropic")
	viper.Set("provider.thinking-level", "")

	t.Cleanup(func() {
		viper.Set("provider.kind", "openai")
		viper.Set("provider.thinking-level", "")
	})

	pc := makeProviderConfig()
	if pc.ThinkingLevel != "medium" {
		t.Errorf("ThinkingLevel = %q, want medium (anthropic default)", pc.ThinkingLevel)
	}
}

// TestMakeProviderConfig_PreservesExplicitOff is the regression net for
// the "do not silently upgrade" constraint. A user who has written
// `thinking-level: "off"` in their config for an anthropic deployment
// must keep getting "off", not get bumped to "medium".
func TestMakeProviderConfig_PreservesExplicitOff(t *testing.T) {
	viper.Set("provider.kind", "anthropic")
	viper.Set("provider.thinking-level", "off")

	t.Cleanup(func() {
		viper.Set("provider.kind", "openai")
		viper.Set("provider.thinking-level", "")
	})

	pc := makeProviderConfig()
	if pc.ThinkingLevel != "off" {
		t.Errorf("ThinkingLevel = %q, want off (explicit user value must be preserved)", pc.ThinkingLevel)
	}
}

// TestMakeProviderConfig_OpenAIKeepsOff verifies that the OpenAI-compatible
// path is unaffected by the per-kind change: an openai user who has
// not configured --provider.thinking-level keeps the historical "off"
// default.
func TestMakeProviderConfig_OpenAIKeepsOff(t *testing.T) {
	viper.Set("provider.kind", "openai")
	viper.Set("provider.thinking-level", "")

	t.Cleanup(func() {
		viper.Set("provider.kind", "openai")
		viper.Set("provider.thinking-level", "")
	})

	pc := makeProviderConfig()
	if pc.ThinkingLevel != "off" {
		t.Errorf("ThinkingLevel = %q, want off (openai default)", pc.ThinkingLevel)
	}
}
