package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/andrewhowdencom/ore/agent"
	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/state"
	"github.com/andrewhowdencom/ore/x/compaction"
	slash "github.com/andrewhowdencom/ore/x/slash"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	settitle "github.com/andrewhowdencom/ore/x/tool/set_title"
	"github.com/andrewhowdencom/ore/x/tool/skills"

	"github.com/andrewhowdencom/workshop/internal/role"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProvider_MissingAPIKey(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", Model: "gpt-4o"}
	_, err := newProvider("openai-test", &pc, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if err.Error() != "missing required provider config: api_key" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_MissingModel(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", APIKey: "sk-test"}
	_, err := newProvider("openai-test", &pc, nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if err.Error() != "missing required provider config: model" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_UnsupportedKind(t *testing.T) {
	pc := ProviderConfig{Kind: "unsupported", APIKey: "sk-test", Model: "gpt-4o"}
	_, err := newProvider("unsupported-test", &pc, nil)
	if err == nil {
		t.Fatal("expected error for unsupported provider kind")
	}
	want := `unsupported provider kind: "unsupported"`
	if err.Error() != want {
		t.Errorf("unexpected error message: %q, want %q", err.Error(), want)
	}
}

func TestNewProvider_Anthropic_MissingAPIKey(t *testing.T) {
	pc := ProviderConfig{Kind: "anthropic", Model: "claude-sonnet-4-5"}
	_, err := newProvider("anthropic-test", &pc, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if err.Error() != "missing required provider config: api_key" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_Anthropic_MissingModel(t *testing.T) {
	pc := ProviderConfig{Kind: "anthropic", APIKey: "sk-ant-test"}
	_, err := newProvider("anthropic-test", &pc, nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if err.Error() != "missing required provider config: model" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_Anthropic_Constructs(t *testing.T) {
	pc := ProviderConfig{Kind: "anthropic", APIKey: "sk-ant-test", Model: "claude-sonnet-4-5"}
	prov, err := newProvider("anthropic-test", &pc, nil)
	if err != nil {
		t.Fatalf("newProvider error: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil provider for valid anthropic config")
	}
}

func TestNewProvider_Anthropic_OpenRouterBaseURL(t *testing.T) {
	// Smoke test: an OpenRouter base URL must not break construction. The
	// auth-header dispatch is verified by the anthropic package's own
	// tests (TestNew_OpenRouterBaseURL); workshop only needs to confirm
	// the option is forwarded.
	pc := ProviderConfig{
		Kind:    "anthropic",
		APIKey:  "sk-or-test",
		Model:   "anthropic/claude-sonnet-4-5",
		BaseURL: "https://openrouter.ai/api/v1",
	}
	prov, err := newProvider("openrouter-test", &pc, nil)
	if err != nil {
		t.Fatalf("newProvider error: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil provider for valid anthropic+openrouter config")
	}
}

// (The three "WarnsOnMaxTokensLeqThinkingBudget" tests were removed
// when the absolute ThinkingBudget knob was replaced with the
// portable ThinkingLevel. The level's percentage-of-max_tokens
// translation enforces the floor/ceiling invariants inside the
// adapter, so no application-side warning is needed.)
//
// TestNewProvider_Anthropic_AppliesDefaultMaxTokens was removed in the
// ore v0.12 migration: newProvider no longer mutates pc.MaxTokens.
// The Anthropic 32k default now lives on the per-turn spec, applied
// by buildDefaultSpec; coverage is in TestBuildDefaultSpec.

// optionTypes returns a slice of %T-formatted type names for the supplied
// options. Tests use this to assert that buildInvokeOptions produced the
// expected per-provider option types without depending on unexported fields.
func optionTypes(opts []provider.InvokeOption) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = fmt.Sprintf("%T", o)
	}
	return out
}

// TestBuildInvokeOptions_OpenAI_IncludesTools verifies that the openai
// path of buildInvokeOptions carries an openai.Tools option (the
// shared provider.ToolsOption wrapper is type-erased).
func TestBuildInvokeOptions_OpenAI_IncludesTools(t *testing.T) {
	cfg := &config{
		providers: map[string]ProviderConfig{
			"test": {Kind: "openai"},
		},
		defaultProviderName: "test",
	}
	got := optionTypes(buildInvokeOptions(cfg, nil))
	foundTools := false
	for _, ty := range got {
		if ty == "provider.ToolsOption" || ty == "*provider.toolsOption" {
			foundTools = true
		}
	}
	if !foundTools {
		t.Errorf("expected a tools option on the openai path; got %v", got)
	}
}

// TestBuildInvokeOptions_Anthropic_IncludesTools verifies that the
// anthropic path of buildInvokeOptions carries the provider.ToolsOption.
// In ore v0.12 model identity, sampling params, output budget, and
// thinking level are all carried on models.Spec and configured on the
// loop via loop.WithDefaultSpec; buildInvokeOptions is reduced to the
// provider-specific options that have no spec equivalent.
func TestBuildInvokeOptions_Anthropic_IncludesTools(t *testing.T) {
	cfg := &config{
		providers: map[string]ProviderConfig{
			"test": {Kind: "anthropic"},
		},
		defaultProviderName: "test",
	}
	got := optionTypes(buildInvokeOptions(cfg, nil))
	foundTools := false
	for _, ty := range got {
		if ty == "provider.ToolsOption" || ty == "*provider.toolsOption" {
			foundTools = true
		}
	}
	if !foundTools {
		t.Errorf("expected a tools option on the anthropic path; got %v", got)
	}
}

// TestBuildInvokeOptions_DoesNotIncludePerProviderSampling is a guard
// against regressing the spec migration: temperature, max-tokens and
// thinking-level are now on the spec, so they must NOT appear as
// InvokeOptions anymore.
func TestBuildInvokeOptions_DoesNotIncludePerProviderSampling(t *testing.T) {
	cfg := &config{
		providers: map[string]ProviderConfig{
			"test": {
				Kind:          "anthropic",
				MaxTokens:     16000,
				ThinkingLevel: "high",
				Temperature:   0.3,
			},
		},
		defaultProviderName: "test",
	}
	got := optionTypes(buildInvokeOptions(cfg, nil))
	for _, ty := range got {
		switch ty {
		case "anthropic.temperatureOption",
			"anthropic.maxTokensOption",
			"anthropic.thinkingLevelOption",
			"openai.temperatureOption",
			"openai.thinkingLevelOption":
			t.Errorf("sampling should live on Spec, not InvokeOptions; saw %s in %v", ty, got)
		}
	}
}

// TestResolveThinkingLevel is a focused unit test for the helper
// that parses user-supplied level strings. The empty string and
// unknown values must both map to ThinkingLevelOff so callers do
// not need to defensively check the parse result.
func TestResolveThinkingLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want models.ThinkingLevel
	}{
		{"", models.ThinkingLevelOff},
		{"off", models.ThinkingLevelOff},
		{"minimal", models.ThinkingLevelMinimal},
		{"low", models.ThinkingLevelLow},
		{"medium", models.ThinkingLevelMedium},
		{"high", models.ThinkingLevelHigh},
		{"max", models.ThinkingLevelMax},
		{"MEDIUM", models.ThinkingLevelOff}, // case-sensitive
		{"foo", models.ThinkingLevelOff},    // unknown -> off
		{" off", models.ThinkingLevelOff},   // whitespace-sensitive
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolveThinkingLevel(tc.in), "input %q", tc.in)
	}
}

// TestBuildDefaultSpec covers the workshop-side defaulting policy:
// Name is taken from the model, Temperature is forwarded as *float64
// (nil when the user left it at zero), ThinkingLevel is parsed and
// normalized, and the Anthropic 32k MaxOutputTokens default kicks in
// when the user leaves MaxTokens at zero. OpenAI does not get a
// default.
func TestBuildDefaultSpec(t *testing.T) {
	t.Run("anthropic applies default max tokens", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Kind:      "anthropic",
			Model:     "claude-sonnet-4-5",
			MaxTokens: 0,
		})
		assert.Equal(t, "claude-sonnet-4-5", got.Name)
		assert.Equal(t, defaultAnthropicMaxTokens, got.MaxOutputTokens)
	})

	t.Run("anthropic honors explicit max tokens", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Kind:      "anthropic",
			Model:     "claude-sonnet-4-5",
			MaxTokens: 16000,
		})
		assert.Equal(t, int64(16000), got.MaxOutputTokens)
	})

	t.Run("openai does not apply default max tokens", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Kind:      "openai",
			Model:     "gpt-4o",
			MaxTokens: 0,
		})
		assert.Equal(t, int64(0), got.MaxOutputTokens)
	})

	t.Run("temperature forwarded as pointer", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Model:       "gpt-4o",
			Temperature: 0.7,
		})
		if assert.NotNil(t, got.Temperature) {
			assert.Equal(t, 0.7, *got.Temperature)
		}
	})

	t.Run("zero temperature leaves spec field nil", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{Model: "gpt-4o"})
		assert.Nil(t, got.Temperature)
	})

	t.Run("thinking level is normalized", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Model:         "gpt-4o",
			ThinkingLevel: "high",
		})
		assert.Equal(t, models.ThinkingLevelHigh, got.ThinkingLevel)
	})

	t.Run("unknown thinking level clamps to off", func(t *testing.T) {
		got := buildDefaultSpec(ProviderConfig{
			Model:         "gpt-4o",
			ThinkingLevel: "frobnicate",
		})
		assert.Equal(t, models.ThinkingLevelOff, got.ThinkingLevel)
	})
}

func TestRoleResolverPath_FallbackWhenEmpty(t *testing.T) {
	rdir := t.TempDir()
	resolver := source.NewFileResolver("")

	// With an empty path, the body should be the default prompt.
	// Mirror what makeSystemPromptTransform does internally.
	path := resolver.Path()
	if path != "" {
		t.Fatalf("path = %q, want empty", path)
	}
	// The fallback is defaultPrompt; we don't re-derive it here since
	// the constant lives in app.go. Just verify the contract: empty
	// path means the resolver has not been initialised with a role.
	if _, err := role.LoadBody(filepath.Join(rdir, "missing.md"), nil); err == nil {
		t.Fatal("LoadBody on missing file should error")
	}
}

func TestRoleResolverPath_TracksSetPath(t *testing.T) {
	rdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rdir, "reviewer.md"), []byte("---\nname: reviewer\n---\nYou are a reviewer.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := source.NewFileResolver("")
	resolver.SetPath(filepath.Join(rdir, "reviewer.md"))

	body, err := role.LoadBody(resolver.Path(), nil)
	if err != nil {
		t.Fatalf("LoadBody error: %v", err)
	}
	if body != "You are a reviewer." {
		t.Errorf("body = %q, want %q", body, "You are a reviewer.")
	}
}

func TestRoleSlashHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	// Valid role (first set on a fresh thread): the resolver's path
	// is updated and the role metadata is recorded. No turn is
	// appended to the conversation; the system prompt transform
	// reflects the role change on the next turn via the resolver.
	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "reviewer"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	assert.Equal(t, "Role: reviewer", res.Notice.Content, "successful set should confirm the new role")

	v, ok := stream.GetMetadata("workshop.role")
	if !ok || v != "reviewer" {
		t.Errorf("metadata = %q, want reviewer", v)
	}

	if got := filepath.Join(dir, "reviewer.md"); rc.Resolver().Path() != got {
		t.Errorf("resolver path = %q, want %q", rc.Resolver().Path(), got)
	}

	if got := len(stream.Turns()); got != 0 {
		t.Errorf("len(turns) = %d, want 0 (no persistent handoff turn)", got)
	}

	// Switching to the same role is a no-op: the resolver path is
	// already correct, no additional turn is appended.
	res, err = rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "reviewer"})
	if err != nil {
		t.Fatalf("handler error on no-op: %v", err)
	}
	if got := len(stream.Turns()); got != 0 {
		t.Errorf("len(turns) after no-op = %d, want 0 (unchanged)", got)
	}
	assert.Equal(t, "Role: reviewer", res.Notice.Content, "no-op should still confirm the active role")

	// Invalid role returns an error (preserves the long-standing contract
	// that switching to a missing role is a hard failure) and does not
	// mutate the state. No new turn is appended on failure.
	_, err = rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	assert.Contains(t, err.Error(), "nonexistent", "error should mention the unknown role name")
	if got := len(stream.Turns()); got != 0 {
		t.Errorf("invalid role should not append a turn: len(turns) = %d, want 0", got)
	}
}

// newRoleCommandStream creates a session stream suitable for the
// role-command tests below. The test provider is a no-op; the role
// handler does not invoke the LLM.
func newRoleCommandStream(t *testing.T) *session.Stream {
	t.Helper()
	store := session.NewMemoryStore()
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})
	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	return stream
}

func TestRoleCommand_NoArgListsRoles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planner.md"), []byte("---\ndescription: Plans multi-step work\n---\nBody.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &roleCommand{rdir: dir}
	rc.SetStream(newRoleCommandStream(t))

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: ""})
	require.NoError(t, err, "no-arg form must not return an error")
	assert.Contains(t, res.Notice.Content, "Role: (none)", "no current role should render as (none)")
	assert.Contains(t, res.Notice.Content, "  planner (Plans multi-step work)", "description from frontmatter should be shown")
	assert.Contains(t, res.Notice.Content, "  reviewer", "role with no description should still appear")
	assert.Contains(t, res.Notice.Content, "Usage: /role <name>", "usage hint should be present")
}

func TestRoleCommand_HelpArgListsRoles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &roleCommand{rdir: dir}
	rc.SetStream(newRoleCommandStream(t))

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "help"})
	require.NoError(t, err, "/role help must not return an error")
	assert.Contains(t, res.Notice.Content, "  reviewer", "help form should list roles")
}

func TestRoleCommand_NoArgShowsCurrentRole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stream := newRoleCommandStream(t)
	stream.SetMetadata("workshop.role", "reviewer")

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: ""})
	require.NoError(t, err)
	assert.Contains(t, res.Notice.Content, "Role: reviewer", "should show the active role")
}

func TestRoleCommand_NoArgEmptyDir(t *testing.T) {
	dir := t.TempDir()
	rc := &roleCommand{rdir: dir}
	rc.SetStream(newRoleCommandStream(t))

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: ""})
	require.NoError(t, err)
	assert.Contains(t, res.Notice.Content, "No roles available in", "empty dir should produce a helpful message")
	assert.Contains(t, res.Notice.Content, dir, "the message should point at the configured directory")
}

func TestRoleCommand_NoArgDoesNotMutateStream(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stream := newRoleCommandStream(t)
	stream.SetMetadata("workshop.role", "reviewer")

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	_, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: ""})
	require.NoError(t, err)

	got, _ := stream.GetMetadata("workshop.role")
	assert.Equal(t, "reviewer", got, "no-arg form must not change the active role")
}

// newRoleCommandStreamWithRoles creates a session stream plus two role
// files on disk (ideation and planner) for the role-switching tests
// below. The stream has no role set; callers set it explicitly when
// they need a non-empty starting role.
func newRoleCommandStreamWithRoles(t *testing.T) (*session.Stream, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"ideation", "planner"} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("Prompt "+name+".\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return newRoleCommandStream(t), dir
}

func TestRoleCommand_UpdateResolver(t *testing.T) {
	stream, dir := newRoleCommandStreamWithRoles(t)
	stream.SetMetadata("workshop.role", "ideation")

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "planner"})
	require.NoError(t, err)
	assert.Equal(t, "Role: planner", res.Notice.Content)

	// The resolver's path should now point to the new role.
	want := filepath.Join(dir, "planner.md")
	assert.Equal(t, want, rc.Resolver().Path(), "resolver should track the new role")

	// No turn should have been appended to the conversation. The
	// system prompt transform reflects the role change on the next
	// turn; persisting a handoff turn would stack the previous role
	// body in conversation history.
	turns := stream.Turns()
	if len(turns) != 0 {
		t.Errorf("len(turns) = %d, want 0 (no persistent handoff turn)", len(turns))
	}
}

func TestRoleCommand_SameRoleDoesNotChangeResolver(t *testing.T) {
	stream, dir := newRoleCommandStreamWithRoles(t)
	stream.SetMetadata("workshop.role", "planner")

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)
	initialPath := rc.Resolver().Path()

	res, err := rc.Handler(context.Background(), nil, slash.Command{Name: "role", Input: "planner"})
	require.NoError(t, err)
	assert.Equal(t, "Role: planner", res.Notice.Content)

	// The path is unchanged. SetPath is called but with the same value.
	assert.Equal(t, initialPath, rc.Resolver().Path())

	turns := stream.Turns()
	if len(turns) != 0 {
		t.Errorf("len(turns) = %d, want 0", len(turns))
	}
}

func TestRoleCommand_SetStreamSeedsResolverFromMetadata(t *testing.T) {
	stream, dir := newRoleCommandStreamWithRoles(t)
	stream.SetMetadata("workshop.role", "planner")

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	want := filepath.Join(dir, "planner.md")
	assert.Equal(t, want, rc.Resolver().Path(), "SetStream should seed the resolver from metadata")
}

// TestCompactSlashHandler_ZeroBudgetStillCompacts verifies that /compact
// is reachable when compaction.max-tokens is 0. Per the explicit-only
// compaction contract, MaxTokens is a pure per-call output budget; a 0
// value means "use the ore/compaction framework default" (8192), not
// "disable /compact". The handler must succeed and the stream must gain
// a summary turn.
func TestCompactSlashHandler_ZeroBudgetStillCompacts(t *testing.T) {
	store := session.NewMemoryStore()
	prov := &testSummarizeProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	// Pre-populate the stream with 5 user turns.
	for i := 0; i < 5; i++ {
		err = stream.Process(context.Background(), session.UserMessageEvent{Content: fmt.Sprintf("message %d", i)})
		if err != nil {
			t.Fatalf("process event %d: %v", i, err)
		}
	}

	// MaxOutputTokens: 0 mirrors compaction.max-tokens: 0. The handler
	// must not error on this and must still produce a summary turn.
	cc := &compactCommand{
		agent: agent.New(
			"test-compactor",
			agent.WithProvider(prov),
			agent.WithSpec(models.Spec{Name: "test-model", MaxOutputTokens: 0}),
			agent.WithPattern(&cognitive.SingleShot{}),
		),
	}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), nil, slash.Command{Name: "compact", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got := stream.Turns()
	if len(got) != 6 {
		t.Fatalf("expected 6 turns (5 original + 1 compaction), got %d", len(got))
	}
	if got[5].Role != state.RoleSystem {
		t.Errorf("compaction turn role = %v, want RoleSystem", got[5].Role)
	}
}

func TestCompactSlashHandler_Enabled(t *testing.T) {
	store := session.NewMemoryStore()
	prov := &testSummarizeProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	// Pre-populate the stream with 5 user turns.
	for i := 0; i < 5; i++ {
		err = stream.Process(context.Background(), session.UserMessageEvent{Content: fmt.Sprintf("message %d", i)})
		if err != nil {
			t.Fatalf("process event %d: %v", i, err)
		}
	}

	turns := stream.Turns()
	if len(turns) != 5 {
		t.Fatalf("expected 5 turns, got %d", len(turns))
	}

	// In ore v0.12 compaction is explicit-only. /compact calls
	// compaction.Summarize, then appends the resulting RoleSystem turn
	// (carrying both artifact.Compaction metadata and the summary
	// artifact.Text) to the stream via AppendTurn. The pre-existing
	// turns remain in the buffer unchanged; the compaction transform
	// projects the LLM-facing view through the new marker.
	cc := &compactCommand{
		agent: agent.New(
			"test-compactor",
			agent.WithProvider(prov),
			agent.WithSpec(models.Spec{Name: "test-model"}),
			agent.WithPattern(&cognitive.SingleShot{}),
		),
	}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), nil, slash.Command{Name: "compact", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got := stream.Turns()
	if len(got) != 6 {
		t.Fatalf("expected 6 turns (5 original + 1 compaction), got %d", len(got))
	}
	compactionTurn := got[5]
	if compactionTurn.Role != state.RoleSystem {
		t.Errorf("compaction turn role = %v, want RoleSystem", compactionTurn.Role)
	}
	// Artifacts order: artifact.Compaction metadata first, then artifact.Text.
	var summary string
	for _, a := range compactionTurn.Artifacts {
		if txt, ok := a.(artifact.Text); ok {
			summary = txt.Content
			break
		}
	}
	if summary != "summary" {
		t.Errorf("compaction turn summary = %q, want %q", summary, "summary")
	}
}

// testEmitter records events emitted through the loop.Emitter interface so
// tests can assert what the slash handler sent to the session stream.
type testEmitter struct {
	mu     sync.Mutex
	events []loop.OutputEvent
}

func (e *testEmitter) Emit(ctx context.Context, ev loop.OutputEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func TestNameSlashHandler_ValidInput_EmitsPropertiesEvent(t *testing.T) {
	emitter := &testEmitter{}
	handler := settitle.Slash()

	result, err := handler(context.Background(), emitter, slash.Command{Name: "name", Input: "Fix login bug"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.Notice.Content != "" {
		t.Errorf("Notice = %q, want empty on valid input", result.Notice.Content)
	}
	if result.Replace != nil {
		t.Errorf("Replace = %v, want nil on valid input", result.Replace)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(emitter.events))
	}
	pe, ok := emitter.events[0].(loop.PropertiesEvent)
	if !ok {
		t.Fatalf("expected loop.PropertiesEvent, got %T", emitter.events[0])
	}
	if got, want := pe.Properties["title"], "Fix login bug"; got != want {
		t.Errorf("Properties[title] = %q, want %q", got, want)
	}
}

func TestNameSlashHandler_EmptyInput_ReturnsFeedback(t *testing.T) {
	emitter := &testEmitter{}
	handler := settitle.Slash()

	result, err := handler(context.Background(), emitter, slash.Command{Name: "name", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.Notice.Content != "Usage: /name <text>" {
		t.Errorf("Notice = %q, want %q", result.Notice.Content, "Usage: /name <text>")
	}
	if result.Replace != nil {
		t.Errorf("Replace = %v, want nil on empty input", result.Replace)
	}
	if len(emitter.events) != 0 {
		t.Errorf("expected no events on empty input, got %d", len(emitter.events))
	}
}

func TestNameSlashHandler_WhitespaceInput_ReturnsFeedback(t *testing.T) {
	emitter := &testEmitter{}
	handler := settitle.Slash()

	result, err := handler(context.Background(), emitter, slash.Command{Name: "name", Input: "   \t  "})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.Notice.Content != "Usage: /name <text>" {
		t.Errorf("Notice = %q, want %q", result.Notice.Content, "Usage: /name <text>")
	}
	if len(emitter.events) != 0 {
		t.Errorf("expected no events on whitespace input, got %d", len(emitter.events))
	}
}

func TestNameSlashHandler_TrimsInput(t *testing.T) {
	emitter := &testEmitter{}
	handler := settitle.Slash()

	_, err := handler(context.Background(), emitter, slash.Command{Name: "name", Input: "  spaced  "})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(emitter.events))
	}
	pe := emitter.events[0].(loop.PropertiesEvent)
	if got, want := pe.Properties["title"], "spaced"; got != want {
		t.Errorf("Properties[title] = %q, want %q", got, want)
	}
}

type testSlashProvider struct{}

func (p *testSlashProvider) Invoke(ctx context.Context, s state.State, spec models.Spec, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	return nil
}

type testSummarizeProvider struct{}

func (p *testSummarizeProvider) Invoke(ctx context.Context, s state.State, spec models.Spec, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	ch <- artifact.Text{Content: "summary"}
	ch <- artifact.StopReason{Reason: artifact.StopReasonStop}
	return nil
}

// TestStatusZoneMapping_ThinkingInLifecycle asserts that the
// "thinking" status key emitted by x/usage/handler.go is routed
// into the "lifecycle" zone, so the framework's compactTokenSegments
// folds it into the same ↑ / ↓ / Σ / Ψ cluster as the other token
// counters. Without this entry, "thinking" falls into the "default"
// zone and renders as an orphan "tokens: Ψ N" segment on its own
// status line, separated from sent / received / total.
func TestStatusZoneMapping_ThinkingInLifecycle(t *testing.T) {
	if got, want := statusZoneMapping["thinking"], "lifecycle"; got != want {
		t.Errorf("statusZoneMapping[thinking] = %q, want %q (thinking token must share zone with other token counters)", got, want)
	}
	for _, k := range []string{"sent", "received", "total"} {
		if got, want := statusZoneMapping[k], "lifecycle"; got != want {
			t.Errorf("statusZoneMapping[%s] = %q, want %q (token key out of zone)", k, got, want)
		}
	}
}

func TestRoleToolSchemas(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		checks func(t *testing.T, schema map[string]any)
	}{
		{
			name:   "createWorkspaceSchema",
			schema: createWorkspaceSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("type = %v, want object", schema["type"])
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Fatal("missing properties")
				}
				if _, ok := props["branch"]; !ok {
					t.Fatal("missing branch property")
				}
				if _, ok := props["base_branch"]; !ok {
					t.Fatal("missing base_branch property")
				}
				reqRaw, ok := schema["required"].([]interface{})
				if !ok {
					t.Fatalf("required is not an array: %T", schema["required"])
				}
				found := false
				for _, r := range reqRaw {
					if s, ok := r.(string); ok && s == "branch" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("required does not contain 'branch': %v", reqRaw)
				}
			},
		},
		{
			name:   "destroyWorkspaceSchema",
			schema: destroyWorkspaceSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("type = %v, want object", schema["type"])
				}
				if _, ok := schema["properties"]; ok {
					t.Error("destroyWorkspaceSchema should have no properties")
				}
			},
		},
		{
			name:   "gitCommitSchema",
			schema: gitCommitSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("type = %v, want object", schema["type"])
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Fatal("missing properties")
				}
				if _, ok := props["title"]; !ok {
					t.Fatal("missing title property")
				}
				if _, ok := props["message"]; !ok {
					t.Fatal("missing message property")
				}
				reqRaw, ok := schema["required"].([]interface{})
				if !ok {
					t.Fatalf("required is not an array: %T", schema["required"])
				}
				found := false
				for _, r := range reqRaw {
					if s, ok := r.(string); ok && s == "title" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("required does not contain 'title': %v", reqRaw)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.schema)
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			tt.checks(t, parsed)
		})
	}
}

func TestBuildManager_Smoke(t *testing.T) {
	mgr, err := buildManager(&config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

func TestBuildManager_WithCompaction(t *testing.T) {
	mgr, err := buildManager(&config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
		compaction: CompactionConfig{
			MaxTokens: 50000,
		},
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

func TestBuildManager_WithWorkingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("project instructions"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr, err := buildManager(&config{
		storeDir:   t.TempDir(),
		workingDir: dir,
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

func TestBuildManager_SeedsRoleForNewThread(t *testing.T) {
	mgr, err := buildManager(&config{
		storeDir: t.TempDir(),
		role:     "reviewer",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	workshopRole, ok := stream.GetMetadata("workshop.role")
	if !ok {
		t.Fatal("workshop.role not seeded for new thread")
	}
	if workshopRole != "reviewer" {
		t.Errorf("workshop.role = %q, want reviewer", workshopRole)
	}
	// The old "role" key should not be seeded for new threads
	if _, ok := stream.GetMetadata("role"); ok {
		t.Error("role should not be seeded for new threads (use workshop.role only)")
	}
}

func TestBuildManager_PreservesExistingRoleOnAttach(t *testing.T) {
	storeDir := t.TempDir()

	// First session: create with role "reviewer"
	mgr1, err := buildManager(&config{
		storeDir: storeDir,
		role:     "reviewer",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}

	stream1, err := mgr1.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	threadID := stream1.ID()

	// Simulate role change during session (like /role command)
	stream1.SetMetadata("workshop.role", "writer")
	if err := stream1.Save(); err != nil {
		t.Fatalf("save stream: %v", err)
	}
	if err := mgr1.Close(threadID); err != nil {
		t.Fatalf("close stream: %v", err)
	}

	// Second session: attach with different role "planner"
	mgr2, err := buildManager(&config{
		storeDir: storeDir,
		role:     "planner",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}

	stream2, err := mgr2.Attach(threadID)
	if err != nil {
		t.Fatalf("attach stream: %v", err)
	}

	workshopRole, _ := stream2.GetMetadata("workshop.role")
	if workshopRole != "writer" {
		t.Errorf("workshop.role = %q, want writer (preserved from previous session)", workshopRole)
	}
	// The old "role" key should not be set; only workshop.role is the canonical key
	if _, ok := stream2.GetMetadata("role"); ok {
		t.Error("role should not be seeded for attached threads (use workshop.role only)")
	}
}

func TestCoAuthoredByTrailer(t *testing.T) {
	tests := []struct {
		name string
		pc   ProviderConfig
		want string
	}{
		{
			name: "simple model",
			pc:   ProviderConfig{Kind: "openai", Model: "gpt-4o"},
			want: "Co-authored-by: gpt-4o <gpt-4o@workshop.agent>",
		},
		{
			name: "provider prefix stripped",
			pc:   ProviderConfig{Kind: "fireworks", Model: "fireworks/kimi-k2p6"},
			want: "Co-authored-by: fireworks/kimi-k2p6 <kimi-k2p6@workshop.agent>",
		},
		{
			name: "claude model",
			pc:   ProviderConfig{Kind: "anthropic", Model: "claude-3-5-sonnet"},
			want: "Co-authored-by: claude-3-5-sonnet <claude-3-5-sonnet@workshop.agent>",
		},
		{
			name: "empty model",
			pc:   ProviderConfig{Kind: "openai", Model: ""},
			want: "",
		},
		{
			name: "empty kind",
			pc:   ProviderConfig{Kind: "", Model: "gpt-4o"},
			want: "",
		},
		{
			name: "multiple slashes",
			pc:   ProviderConfig{Kind: "custom", Model: "a/b/c/model"},
			want: "Co-authored-by: a/b/c/model <model@workshop.agent>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coAuthoredByTrailer(tt.pc)
			if got != tt.want {
				t.Errorf("coAuthoredByTrailer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMakeWorkspaceCreateHandler_MissingBranch(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	handler := makeWorkspaceCreateHandler(thr)
	_, err = handler(context.Background(), nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing branch")
	}
}

func TestMakeWorkspaceDestroyHandler_NoWorktree(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	handler := makeWorkspaceDestroyHandler(thr)
	_, err = handler(context.Background(), nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error when no worktree was created")
	}
	if !strings.Contains(err.Error(), "no worktree") {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestMakeGitCommitHandler_MissingTitle(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	handler := makeGitCommitHandler(thr, ProviderConfig{Kind: "openai", Model: "gpt-4o"})
	_, err = handler(context.Background(), nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing title")
	}
	_, err = handler(context.Background(), nil, map[string]any{"title": "   "})
	if err == nil {
		t.Fatal("expected error for empty/whitespace title")
	}
}

func TestMakeWorkspaceCreateDestroyIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in test environment")
	}

	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// Create worktree.
	createHandler := makeWorkspaceCreateHandler(thr)
	result, err := createHandler(context.Background(), nil, map[string]any{"branch": "feature"})
	if err != nil {
		t.Fatalf("workspace_create failed: %v", err)
	}
	path, ok := result.(string)
	if !ok || path == "" {
		t.Fatalf("unexpected result type: %T", result)
	}

	// Verify metadata stored.
	meta, ok := thr.Metadata["workshop.worktree.path"]
	if !ok || meta != path {
		t.Fatalf("metadata = %q, want %q", meta, path)
	}

	// Verify worktree directory exists.
	wtPath := filepath.Join(dir, path)
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree directory does not exist: %v", err)
	}

	// Destroy worktree.
	destroyHandler := makeWorkspaceDestroyHandler(thr)
	_, err = destroyHandler(context.Background(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("workspace_destroy failed: %v", err)
	}

	// Verify metadata cleared.
	meta, ok = thr.Metadata["workshop.worktree.path"]
	if ok && meta != "" {
		t.Fatalf("metadata should be cleared, got %q", meta)
	}

	// Verify worktree directory removed.
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory should not exist: %v", err)
	}
}

func TestMakeGitCommitHandler_Integration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in test environment")
	}

	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Modify and stage a file.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "add", "file.txt").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}

	// Run handler in the temp repo.
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	pc := ProviderConfig{Kind: "openai", Model: "gpt-4o"}
	handler := makeGitCommitHandler(thr, pc)
	_, err = handler(context.Background(), nil, map[string]any{"title": "Update greeting", "message": "Changed text"})
	if err != nil {
		t.Fatalf("git_commit failed: %v", err)
	}

	// Verify commit contains trailer.
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	wantTrailer := "Co-authored-by: gpt-4o <gpt-4o@workshop.agent>"
	if !strings.Contains(string(out), wantTrailer) {
		t.Errorf("commit message missing trailer:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Changed text") {
		t.Errorf("commit message missing body:\n%s", string(out))
	}
}

func TestSystemPrompt_WithCWD(t *testing.T) {
	cfg := &config{
		workingDir: "/test/project",
		conduit:    "TUI",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	// No role is set in this test; the closure mirrors the production
	// resolver-based dispatch in makeSystemPromptTransform and falls
	// back to defaultPrompt when the resolver path is empty.
	resolver := source.NewFileResolver("")
	currentPrompt := func() string {
		path := resolver.Path()
		if path == "" {
			return defaultPrompt
		}
		body, err := role.LoadBody(path, nil)
		if err != nil {
			return defaultPrompt
		}
		return body
	}

	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(currentPrompt),
		systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	if !strings.Contains(text.Content, "You are running in: /test/project") {
		t.Errorf("prompt does not contain cwd context: %q", text.Content)
	}
	if !strings.Contains(text.Content, defaultPrompt) {
		t.Errorf("prompt does not contain default prompt: %q", text.Content)
	}
}

func TestSystemPrompt_WithoutCWD(t *testing.T) {
	cfg := &config{
		workingDir: "",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	// No role is set in this test; the closure mirrors the production
	// resolver-based dispatch in makeSystemPromptTransform and falls
	// back to defaultPrompt when the resolver path is empty.
	resolver := source.NewFileResolver("")
	currentPrompt := func() string {
		path := resolver.Path()
		if path == "" {
			return defaultPrompt
		}
		body, err := role.LoadBody(path, nil)
		if err != nil {
			return defaultPrompt
		}
		return body
	}

	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(currentPrompt),
		systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	if strings.Contains(text.Content, "You are running in:") {
		t.Errorf("prompt should not contain cwd context when workingDir is empty: %q", text.Content)
	}
}

func TestSystemPrompt_WithAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Repo instructions here."), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: dir,
		conduit:    "TUI",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	// No role is set in this test; the closure mirrors the production
	// resolver-based dispatch in makeSystemPromptTransform and falls
	// back to defaultPrompt when the resolver path is empty.
	resolver := source.NewFileResolver("")
	currentPrompt := func() string {
		path := resolver.Path()
		if path == "" {
			return defaultPrompt
		}
		body, err := role.LoadBody(path, nil)
		if err != nil {
			return defaultPrompt
		}
		return body
	}

	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(currentPrompt),
		systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
		systemprompt.WithContentFunc(source.AgentsMD(cfg.workingDir)),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	if !strings.Contains(text.Content, "Repo instructions here.") {
		t.Errorf("prompt does not contain AGENTS.md content: %q", text.Content)
	}
	if !strings.Contains(text.Content, "You are running in:") {
		t.Errorf("prompt does not contain cwd context: %q", text.Content)
	}
	if !strings.Contains(text.Content, defaultPrompt) {
		t.Errorf("prompt does not contain default prompt: %q", text.Content)
	}
}

func TestSystemPrompt_WithAgentsMDNearestFirst(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "AGENTS.md"), []byte("child instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "CLAUDE.md"), []byte("parent claude"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: child,
		conduit:    "TUI",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	// No role is set in this test; the closure mirrors the production
	// resolver-based dispatch in makeSystemPromptTransform and falls
	// back to defaultPrompt when the resolver path is empty.
	resolver := source.NewFileResolver("")
	currentPrompt := func() string {
		path := resolver.Path()
		if path == "" {
			return defaultPrompt
		}
		body, err := role.LoadBody(path, nil)
		if err != nil {
			return defaultPrompt
		}
		return body
	}

	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(currentPrompt),
		systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
		systemprompt.WithContentFunc(source.AgentsMD(cfg.workingDir)),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	childIdx := strings.Index(text.Content, "child instructions")
	parentIdx := strings.Index(text.Content, "parent claude")
	if childIdx == -1 {
		t.Errorf("prompt does not contain child AGENTS.md content")
	}
	if parentIdx == -1 {
		t.Errorf("prompt does not contain parent CLAUDE.md content")
	}
	if childIdx != -1 && parentIdx != -1 && childIdx > parentIdx {
		t.Errorf("child instructions should appear before parent claude (nearest-first); child at %d, parent at %d", childIdx, parentIdx)
	}
}

func TestMakeSystemPromptTransform_WithAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("repo instructions"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: dir,
		conduit:    "TUI",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit(), source.NewFileResolver(""))
	if err != nil {
		t.Fatalf("makeSystemPromptTransform error: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	// Verify all expected fragments are present.
	if !strings.Contains(text.Content, defaultPrompt) {
		t.Errorf("prompt does not contain default prompt: %q", text.Content)
	}
	if !strings.Contains(text.Content, "You are running in:") {
		t.Errorf("prompt does not contain cwd context: %q", text.Content)
	}
	if !strings.Contains(text.Content, "repo instructions") {
		t.Errorf("prompt does not contain AGENTS.md content: %q", text.Content)
	}
	if !strings.Contains(text.Content, "You are running the workshop agent") {
		t.Errorf("prompt does not contain harness: %q", text.Content)
	}
	if !strings.Contains(text.Content, "You are running on model test-model.") {
		t.Errorf("prompt does not contain model: %q", text.Content)
	}
	if !strings.Contains(text.Content, "Provider backend: openai") {
		t.Errorf("prompt does not contain provider: %q", text.Content)
	}

	// Verify ordering: defaultPrompt < cwd context < agents < harness < model < provider.
	defaultIdx := strings.Index(text.Content, defaultPrompt)
	cwdIdx := strings.Index(text.Content, "You are running in:")
	agentsIdx := strings.Index(text.Content, "repo instructions")
	harnessIdx := strings.Index(text.Content, "You are running the workshop agent")
	modelIdx := strings.Index(text.Content, "You are running on model test-model.")
	providerIdx := strings.Index(text.Content, "Provider backend: openai")
	if defaultIdx == -1 || cwdIdx == -1 || agentsIdx == -1 || harnessIdx == -1 || modelIdx == -1 || providerIdx == -1 {
		t.Fatalf("expected all fragments in prompt; default=%d cwd=%d agents=%d harness=%d model=%d provider=%d", defaultIdx, cwdIdx, agentsIdx, harnessIdx, modelIdx, providerIdx)
	}
	if !(defaultIdx < cwdIdx && cwdIdx < agentsIdx && agentsIdx < harnessIdx && harnessIdx < modelIdx && modelIdx < providerIdx) {
		t.Errorf("fragment ordering incorrect; expected default < cwd < agents < harness < model < provider, got default=%d cwd=%d agents=%d harness=%d model=%d provider=%d", defaultIdx, cwdIdx, agentsIdx, harnessIdx, modelIdx, providerIdx)
	}
}

func TestMakeSystemPromptTransform_NearestFirst(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "AGENTS.md"), []byte("child instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "CLAUDE.md"), []byte("parent claude"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: child,
		conduit:    "TUI",
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit(), source.NewFileResolver(""))
	if err != nil {
		t.Fatalf("makeSystemPromptTransform error: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	childIdx := strings.Index(text.Content, "child instructions")
	parentIdx := strings.Index(text.Content, "parent claude")
	if childIdx == -1 {
		t.Errorf("prompt does not contain child AGENTS.md content")
	}
	if parentIdx == -1 {
		t.Errorf("prompt does not contain parent CLAUDE.md content")
	}
	if childIdx != -1 && parentIdx != -1 && childIdx > parentIdx {
		t.Errorf("child instructions should appear before parent claude (nearest-first); child at %d, parent at %d", childIdx, parentIdx)
	}
}

func TestMakeSystemPromptTransform_NoInstructionFiles(t *testing.T) {
	dir := t.TempDir() // empty directory, no AGENTS.md or CLAUDE.md

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: dir,
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit(), source.NewFileResolver(""))
	if err != nil {
		t.Fatalf("makeSystemPromptTransform error: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	// Should contain default prompt and cwd context but no agents-derived content.
	if !strings.Contains(text.Content, defaultPrompt) {
		t.Errorf("prompt does not contain default prompt: %q", text.Content)
	}
	if !strings.Contains(text.Content, "You are running in:") {
		t.Errorf("prompt does not contain cwd context: %q", text.Content)
	}
	// Verify the prompt does not end with a stray separator that would indicate
	// an empty third content source was still concatenated.
	if strings.HasSuffix(text.Content, "\n\n") {
		t.Errorf("prompt should not end with blank separator when no agents files exist: %q", text.Content)
	}
}

// mockSkillDiscoverer is a test double for skills.Discoverer.
type mockSkillDiscoverer struct {
	meta []skills.SkillMeta
	read map[string]string
}

func (m *mockSkillDiscoverer) Discover(ctx context.Context) ([]skills.SkillMeta, error) {
	return m.meta, nil
}

func (m *mockSkillDiscoverer) Read(ctx context.Context, name string) (string, error) {
	return m.read[name], nil
}

func TestSystemPrompt_WithSkillsFragment(t *testing.T) {
	mock := &mockSkillDiscoverer{
		meta: []skills.SkillMeta{
			{Name: "git", Description: "Guidelines for git operations"},
			{Name: "testing", Description: "Testing best practices"},
		},
	}

	tk := skills.NewToolkit(mock)
	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(func() string { return "Base prompt." }),
		systemprompt.WithContextContentFunc(tk.SystemPromptFragment()),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	if !strings.Contains(text.Content, "git") {
		t.Errorf("prompt does not contain skill 'git': %q", text.Content)
	}
	if !strings.Contains(text.Content, "testing") {
		t.Errorf("prompt does not contain skill 'testing': %q", text.Content)
	}
	if !strings.Contains(text.Content, "When your task matches a skill description below") {
		t.Errorf("prompt does not contain skills fragment directive: %q", text.Content)
	}
	if !strings.Contains(text.Content, "call read_skill") {
		t.Errorf("prompt does not contain read_skill directive: %q", text.Content)
	}
}

func TestSystemPrompt_WithoutSkillsFragment(t *testing.T) {
	mock := &mockSkillDiscoverer{
		meta: []skills.SkillMeta{},
	}

	tk := skills.NewToolkit(mock)
	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(func() string { return "Base prompt." }),
		systemprompt.WithContextContentFunc(tk.SystemPromptFragment()),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}
	if turns[0].Role != state.RoleSystem {
		t.Errorf("expected RoleSystem, got %v", turns[0].Role)
	}
	if len(turns[0].Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(turns[0].Artifacts))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	if strings.Contains(text.Content, "You have access to the following specialized skills") {
		t.Errorf("prompt should not contain skills fragment header when no skills: %q", text.Content)
	}
	if !strings.Contains(text.Content, "Base prompt.") {
		t.Errorf("prompt should contain base prompt: %q", text.Content)
	}
}

// mockSkillDiscovererError always returns an error from Discover.
type mockSkillDiscovererError struct{}

func (m *mockSkillDiscovererError) Discover(ctx context.Context) ([]skills.SkillMeta, error) {
	return nil, fmt.Errorf("simulated discoverer error")
}

func (m *mockSkillDiscovererError) Read(ctx context.Context, name string) (string, error) {
	return "", fmt.Errorf("simulated read error")
}

func TestSystemPrompt_WithSkillsFragmentError(t *testing.T) {
	mock := &mockSkillDiscovererError{}
	tk := skills.NewToolkit(mock)
	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(func() string { return "Base prompt." }),
		systemprompt.WithContextContentFunc(tk.SystemPromptFragment()),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	// Fragment should be omitted on error; only base prompt remains.
	if strings.Contains(text.Content, "You have access to the following specialized skills") {
		t.Errorf("prompt should not contain skills fragment header when discoverer errors: %q", text.Content)
	}
	if !strings.Contains(text.Content, "Base prompt.") {
		t.Errorf("prompt should contain base prompt: %q", text.Content)
	}
}

func TestSystemPrompt_WithCWDAndSkillsFragment(t *testing.T) {
	mock := &mockSkillDiscoverer{
		meta: []skills.SkillMeta{
			{Name: "git", Description: "Guidelines for git operations"},
		},
	}

	tk := skills.NewToolkit(mock)
	sp, err := systemprompt.New(
		systemprompt.WithContentFunc(func() string { return "Base prompt." }),
		systemprompt.WithContentFunc(func() string { return "You are running in: /test/project." }),
		systemprompt.WithContextContentFunc(tk.SystemPromptFragment()),
	)
	if err != nil {
		t.Fatalf("create system prompt: %v", err)
	}

	base := &state.Buffer{}
	result, err := sp.Transform(context.Background(), base)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	turns := result.Turns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 virtual turn, got %d", len(turns))
	}

	text, ok := turns[0].Artifacts[0].(artifact.Text)
	if !ok {
		t.Fatalf("expected artifact.Text, got %T", turns[0].Artifacts[0])
	}

	// Verify all three fragments are present.
	content := text.Content
	if !strings.Contains(content, "Base prompt.") {
		t.Errorf("prompt does not contain base prompt: %q", content)
	}
	if !strings.Contains(content, "You are running in: /test/project.") {
		t.Errorf("prompt does not contain working dir content: %q", content)
	}
	if !strings.Contains(content, "git") {
		t.Errorf("prompt does not contain skill 'git': %q", content)
	}
	if !strings.Contains(content, "When your task matches a skill description below") {
		t.Errorf("prompt does not contain skills fragment directive: %q", content)
	}

	// Verify ordering: base prompt < working dir < skills fragment.
	baseIdx := strings.Index(content, "Base prompt.")
	cwdIdx := strings.Index(content, "You are running in:")
	skillsIdx := strings.Index(content, "When your task matches a skill description below")
	if baseIdx == -1 || cwdIdx == -1 || skillsIdx == -1 {
		t.Fatalf("missing expected fragments in prompt")
	}
	if !(baseIdx < cwdIdx && cwdIdx < skillsIdx) {
		t.Errorf("fragment ordering incorrect: base=%d cwd=%d skills=%d", baseIdx, cwdIdx, skillsIdx)
	}
}

func TestSkillsFragment_RealFSDiscoverer(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := "---\nname: git\ndescription: Guidelines for git operations\n---\n\nGit skill content.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	discoverer := skills.NewFSDiscoverer(skillDir)
	tk := skills.NewToolkit(discoverer)

	fragment := tk.SystemPromptFragment()(context.Background())
	if fragment == "" {
		t.Fatal("expected non-empty fragment from real FS discoverer")
	}
	if !strings.Contains(fragment, "git") {
		t.Errorf("fragment does not contain skill name 'git': %q", fragment)
	}
	if !strings.Contains(fragment, "Guidelines for git operations") {
		t.Errorf("fragment does not contain skill description: %q", fragment)
	}
	if !strings.Contains(fragment, "call read_skill") {
		t.Errorf("fragment does not contain read_skill directive: %q", fragment)
	}
}

// --- Workshop Sandbox Tests ---

func TestWorkshopSandbox_ResolvePath_RelativeInWorktree(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	got, err := sb.ResolvePath("file.txt")
	if err != nil {
		t.Fatalf("ResolvePath error: %v", err)
	}
	want := filepath.Join(worktree, "file.txt")
	if got != want {
		t.Errorf("ResolvePath = %q, want %q", got, want)
	}
}

func TestWorkshopSandbox_ResolvePath_AbsoluteUnchanged(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	absPath := "/etc/passwd"
	got, err := sb.ResolvePath(absPath)
	if err != nil {
		t.Fatalf("ResolvePath error: %v", err)
	}
	if got != absPath {
		t.Errorf("ResolvePath = %q, want %q (unchanged)", got, absPath)
	}
}

func TestWorkshopSandbox_ResolvePath_NoWorktree(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	sb := &workshopSandbox{name: "test", mr: thr}
	relPath := "file.txt"
	got, err := sb.ResolvePath(relPath)
	if err != nil {
		t.Fatalf("ResolvePath error: %v", err)
	}
	if got != relPath {
		t.Errorf("ResolvePath = %q, want %q (unchanged)", got, relPath)
	}
}

func TestWorkshopSandbox_WorkingDirectory_WithWorktree(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	got := sb.WorkingDirectory()
	if got != worktree {
		t.Errorf("WorkingDirectory = %q, want %q", got, worktree)
	}
}

func TestWorkshopSandbox_WorkingDirectory_WithoutWorktree(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	sb := &workshopSandbox{name: "test", mr: thr}
	got := sb.WorkingDirectory()
	if got != "" {
		t.Errorf("WorkingDirectory = %q, want empty string", got)
	}
}

func TestReadFile_ResolvesRelativePathInWorktree(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "file.txt"), []byte("hello worktree"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := filesystem.ReadFile(context.Background(), sb, map[string]any{"path": "file.txt"})
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	content, ok := result.(*filesystem.ReadFileResult)
	if !ok {
		t.Fatalf("result type = %T, want *filesystem.ReadFileResult", result)
	}
	if !strings.Contains(content.Content, "hello worktree") {
		t.Errorf("content = %q, want 'hello worktree'", content.Content)
	}
}

func TestReadFile_AbsolutePathUnchangedInWorktree(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside content"), 0644); err != nil {
		t.Fatal(err)
	}

	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := filesystem.ReadFile(context.Background(), sb, map[string]any{"path": filepath.Join(outside, "outside.txt")})
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	content, ok := result.(*filesystem.ReadFileResult)
	if !ok {
		t.Fatalf("result type = %T, want *filesystem.ReadFileResult", result)
	}
	if !strings.Contains(content.Content, "outside content") {
		t.Errorf("content = %q, want 'outside content'", content.Content)
	}
}

func TestWriteFile_ResolvesRelativePathInWorktree(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	_, err = filesystem.WriteFile(context.Background(), sb, map[string]any{
		"path":    "newfile.txt",
		"content": "written from worktree",
	})
	if err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, "newfile.txt"))
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if string(data) != "written from worktree" {
		t.Errorf("content = %q, want 'written from worktree'", string(data))
	}
}

func TestEditFile_ResolvesRelativePathInWorktree(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "edit.txt"), []byte("old content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	_, err = filesystem.EditFile(context.Background(), sb, map[string]any{
		"path":       "edit.txt",
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("EditFile error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, "edit.txt"))
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if string(data) != "new content\n" {
		t.Errorf("content = %q, want 'new content\\n'", string(data))
	}
}

func TestListDirectory_ResolvesRelativePathInWorktree(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := filesystem.ListDirectory(context.Background(), sb, map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("ListDirectory error: %v", err)
	}

	entries, ok := result.(*filesystem.ListDirectoryResult)
	if !ok {
		t.Fatalf("result type = %T, want *filesystem.ListDirectoryResult", result)
	}
	if len(entries.Entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(entries.Entries))
	}
}

func TestSearchFiles_ResolvesRelativePathInWorktree(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "search.txt"), []byte("match me\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := filesystem.SearchFiles(context.Background(), sb, map[string]any{
		"path":  ".",
		"query": "match",
	})
	if err != nil {
		t.Fatalf("SearchFiles error: %v", err)
	}

	results, ok := result.(*filesystem.SearchFilesResult)
	if !ok {
		t.Fatalf("result type = %T, want *filesystem.SearchFilesResult", result)
	}
	if len(results.Results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results.Results))
	}
}

func TestBash_DefaultsToWorktreeDirectory(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := bash.Bash(context.Background(), sb, map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}

	m, ok := result.(*bash.Result)
	if !ok {
		t.Fatalf("result type = %T, want *bash.Result", result)
	}
	if !strings.Contains(m.Stdout, worktree) {
		t.Errorf("stdout = %q, want to contain %q", m.Stdout, worktree)
	}
}

func TestBash_ExplicitWorkingDirectoryRespected(t *testing.T) {
	worktree := t.TempDir()
	explicitDir := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	sb := &workshopSandbox{name: "test", mr: thr}
	result, err := bash.Bash(context.Background(), sb, map[string]any{
		"command":           "pwd",
		"working_directory": explicitDir,
	})
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}

	m, ok := result.(*bash.Result)
	if !ok {
		t.Fatalf("result type = %T, want *bash.Result", result)
	}
	if !strings.Contains(m.Stdout, explicitDir) {
		t.Errorf("stdout = %q, want to contain %q", m.Stdout, explicitDir)
	}
}

func TestGitCommitHandler_WorktreeAware(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "feature")
	if err := exec.Command("git", "-C", dir, "worktree", "add", "-b", "feature", worktreePath).Run(); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "feature.txt"), []byte("feature content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", worktreePath, "add", "feature.txt").Run(); err != nil {
		t.Fatalf("git add in worktree: %v", err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktreePath

	pc := ProviderConfig{Kind: "openai", Model: "gpt-4o"}
	handler := makeGitCommitHandler(thr, pc)

	sb := &workshopSandbox{name: "test", mr: thr}
	_, err = handler(context.Background(), sb, map[string]any{"title": "Feature commit"})
	if err != nil {
		t.Fatalf("git_commit failed: %v", err)
	}

	out, err := exec.Command("git", "-C", worktreePath, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(string(out), "Feature commit") {
		t.Errorf("commit message missing title:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Co-authored-by: gpt-4o <gpt-4o@workshop.agent>") {
		t.Errorf("commit message missing trailer:\n%s", string(out))
	}
}

func TestWorkspaceCreateHandler_NestedRejection(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = "/some/worktree/path"

	handler := makeWorkspaceCreateHandler(thr)
	_, err = handler(context.Background(), nil, map[string]any{"branch": "nested"})
	if err == nil {
		t.Fatal("expected error for nested workspace_create")
	}
	if !strings.Contains(err.Error(), "already inside worktree") {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestWorkspaceDestroy_RevertsContext(t *testing.T) {
	worktree := t.TempDir()
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.worktree.path"] = worktree

	// Verify sandbox resolves relative paths to worktree
	sb := &workshopSandbox{name: "test", mr: thr}
	got, _ := sb.ResolvePath("file.txt")
	want := filepath.Join(worktree, "file.txt")
	if got != want {
		t.Fatalf("before destroy: ResolvePath = %q, want %q", got, want)
	}

	// Clear metadata (simulating workspace_destroy)
	thr.Metadata["workshop.worktree.path"] = ""

	// Verify sandbox now returns relative paths unchanged
	got, _ = sb.ResolvePath("file.txt")
	if got != "file.txt" {
		t.Errorf("after destroy: ResolvePath = %q, want %q", got, "file.txt")
	}

	// Verify WorkingDirectory is empty
	if dir := sb.WorkingDirectory(); dir != "" {
		t.Errorf("after destroy: WorkingDirectory = %q, want empty", dir)
	}
}

func TestCompactionNotifier(t *testing.T) {
	t.Run("NotifyWithoutReloader", func(t *testing.T) {
		n := &compactionNotifier{}
		n.Notify([]state.Turn{}, compaction.BoundaryInfo{}) // should not panic
	})

	t.Run("NotifyWithReloader", func(t *testing.T) {
		n := &compactionNotifier{}
		var got []state.Turn
		var gotBoundary compaction.BoundaryInfo
		n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			got = turns
			gotBoundary = boundary
		})
		want := []state.Turn{{Role: state.RoleUser}}
		wantBoundary := compaction.BoundaryInfo{Model: "test-model"}
		n.Notify(want, wantBoundary)
		if len(got) != 1 || got[0].Role != state.RoleUser {
			t.Errorf("got %v, want %v", got, want)
		}
		if gotBoundary != wantBoundary {
			t.Errorf("got boundary %+v, want %+v", gotBoundary, wantBoundary)
		}
	})

	t.Run("SetReloaderOverwrites", func(t *testing.T) {
		n := &compactionNotifier{}
		var firstCalled, secondCalled bool
		n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			firstCalled = true
		})
		n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			secondCalled = true
		})
		n.Notify(nil, compaction.BoundaryInfo{})
		if firstCalled {
			t.Error("first reloader was called, expected overwrite")
		}
		if !secondCalled {
			t.Error("second reloader was not called")
		}
	})

	t.Run("NotifyNilTurns", func(t *testing.T) {
		n := &compactionNotifier{}
		var got []state.Turn
		n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			got = turns
		})
		n.Notify(nil, compaction.BoundaryInfo{})
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("ThreadSafety", func(t *testing.T) {
		n := &compactionNotifier{}
		var count int64
		n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			atomic.AddInt64(&count, 1)
		})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n.Notify([]state.Turn{}, compaction.BoundaryInfo{})
			}()
		}
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				n.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
					atomic.AddInt64(&count, 1)
				})
			}(i)
		}
		wg.Wait()

		if count == 0 {
			t.Error("reloader was never called")
		}
	})
}

func TestCompactSlashHandler_Notifies(t *testing.T) {
	store := session.NewMemoryStore()
	prov := &testSummarizeProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	// Pre-populate the stream with 5 user turns.
	for i := 0; i < 5; i++ {
		err = stream.Process(context.Background(), session.UserMessageEvent{Content: fmt.Sprintf("message %d", i)})
		if err != nil {
			t.Fatalf("process event %d: %v", i, err)
		}
	}

	turns := stream.Turns()
	if len(turns) != 5 {
		t.Fatalf("expected 5 turns, got %d", len(turns))
	}

	// In ore v0.12 compaction is explicit-only; /compact calls
	// compaction.Summarize and appends the result. The notifier
	// receives the post-append turn slice (5 original + 1 compaction)
	// and the boundary info for the just-appended summary turn.
	var notified []state.Turn
	var notifiedBoundary compaction.BoundaryInfo
	notifier := &compactionNotifier{}
	notifier.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
		notified = turns
		notifiedBoundary = boundary
	})

	cc := &compactCommand{
		agent: agent.New(
			"test-compactor",
			agent.WithProvider(prov),
			agent.WithSpec(models.Spec{Name: "test-model"}),
			agent.WithPattern(&cognitive.SingleShot{}),
		),
		notifier: notifier,
	}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), nil, slash.Command{Name: "compact", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if len(notified) != 6 {
		t.Fatalf("expected notifier to receive 6 turns (5 original + 1 compaction), got %d", len(notified))
	}
	if notified[5].Role != state.RoleSystem {
		t.Errorf("last notified turn role = %v, want RoleSystem", notified[5].Role)
	}
	// The boundary index should point at the compaction turn (the last one).
	if notifiedBoundary.CompactedThrough != 5 {
		t.Errorf("notified boundary index = %d, want 5", notifiedBoundary.CompactedThrough)
	}

	// The state.Meta boundary keys must be written for downstream
	// Transform / projection to honor the boundary on the next turn.
	st := stream.State()
	if got, _ := st.Meta().Get(compaction.MetaKeyBoundaryIndex); got != "5" {
		t.Errorf("state.Meta[%q] = %q, want %q", compaction.MetaKeyBoundaryIndex, got, "5")
	}
	if _, ok := st.Meta().Get(compaction.MetaKeyBoundaryInfo); !ok {
		t.Errorf("state.Meta[%q] is unset, want it set", compaction.MetaKeyBoundaryInfo)
	}
}

func TestBuildManager_CompactionNotifier(t *testing.T) {
	var notified []state.Turn
	notifier := &compactionNotifier{}
	notifier.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
		notified = turns
	})

	mgr, err := buildManager(&config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"test": {
				Kind:   "openai",
				APIKey: "sk-test-dummy",
				Model:  "test-model",
			},
		},
		defaultProviderName: "test",
		compaction: CompactionConfig{
			MaxTokens: 50000,
		},
		compactionNotifier: notifier,
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}

	// Verify that the notifier is still functional after buildManager.
	testTurns := []state.Turn{{Role: state.RoleUser}}
	notifier.Notify(testTurns, compaction.BoundaryInfo{})
	if len(notified) != 1 || notified[0].Role != state.RoleUser {
		t.Errorf("notifier did not receive test turns: got %v", notified)
	}
}

// newThinkingCommandStream creates a fresh in-memory session manager
// and stream for the thinking-command tests. The provider is a
// no-op; only the slash handler is exercised.
func newThinkingCommandStream(t *testing.T) *session.Stream {
	t.Helper()
	store := session.NewMemoryStore()
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})
	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	return stream
}

func TestThinkingCommand_NoArgReportsCurrent(t *testing.T) {
	stream := newThinkingCommandStream(t)
	tc := &thinkingCommand{}
	tc.SetStream(stream)

	res, err := tc.Handler(context.Background(), nil, slash.Command{Name: "thinking", Input: ""})
	require.NoError(t, err)
	assert.Contains(t, res.Notice.Content, "Thinking: off", "no-arg form should report current level")
	assert.Contains(t, res.Notice.Content, "Levels: off, minimal, low, medium, high, max", "no-arg form should list available levels")
}

func TestThinkingCommand_ValidLevelSetsMetadata(t *testing.T) {
	stream := newThinkingCommandStream(t)
	tc := &thinkingCommand{}
	tc.SetStream(stream)

	res, err := tc.Handler(context.Background(), nil, slash.Command{Name: "thinking", Input: "high"})
	require.NoError(t, err)
	assert.Equal(t, "Thinking: high", res.Notice.Content)

	// Verify the metadata was actually written. GetMetadata returns the
	// value the next read of buildInvokeOptions will see.
	got, ok := stream.GetMetadata("workshop.thinking_level")
	require.True(t, ok, "metadata should be set")
	assert.Equal(t, "high", got)
}

func TestThinkingCommand_InvalidLevelNoOp(t *testing.T) {
	stream := newThinkingCommandStream(t)
	tc := &thinkingCommand{}
	tc.SetStream(stream)

	// Pre-set a known level so we can verify it isn't overwritten.
	stream.SetMetadata("workshop.thinking_level", "medium")

	res, err := tc.Handler(context.Background(), nil, slash.Command{Name: "thinking", Input: "frobnicate"})
	require.NoError(t, err)
	assert.Contains(t, res.Notice.Content, "Unknown level: frobnicate", "should report the unknown level name")
	assert.Contains(t, res.Notice.Content, "Available:", "should list valid levels in the error")

	got, _ := stream.GetMetadata("workshop.thinking_level")
	assert.Equal(t, "medium", got, "metadata must not be mutated by an invalid set")
}

func TestThinkingCommand_OffIsValid(t *testing.T) {
	stream := newThinkingCommandStream(t)
	tc := &thinkingCommand{}
	tc.SetStream(stream)

	// "off" is a valid level that disables thinking; it must be accepted
	// and must write the metadata.
	res, err := tc.Handler(context.Background(), nil, slash.Command{Name: "thinking", Input: "off"})
	require.NoError(t, err)
	assert.Equal(t, "Thinking: off", res.Notice.Content)
	got, _ := stream.GetMetadata("workshop.thinking_level")
	assert.Equal(t, "off", got)
}

func TestThinkingCommand_NoStreamError(t *testing.T) {
	tc := &thinkingCommand{}
	// No SetStream call: simulates invoking /thinking before any stream
	// has been opened. The handler must surface a clear error.
	_, err := tc.Handler(context.Background(), nil, slash.Command{Name: "thinking", Input: "high"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active stream")
}

// TestThinkingCommand_LevelRoundTripsThroughDefaultSpec verifies the
// end-to-end path for a metadata-driven thinking level: /thinking
// writes "high" to stream metadata, the next turn's default spec
// is built from cfg (which was written from the metadata in
// production) and surfaces the parsed level on models.Spec.ThinkingLevel.
// In the ore v0.12 migration this replaces the previous
// buildInvokeOptions-then-thinkLevelOption path: thinking level
// lives on the spec, not on InvokeOptions.
func TestThinkingCommand_LevelRoundTripsThroughDefaultSpec(t *testing.T) {
	stream := newThinkingCommandStream(t)

	// Simulate the user setting the level via /thinking.
	stream.SetMetadata("workshop.thinking_level", "high")

	cfg := &config{
		providers: map[string]ProviderConfig{
			"test": {
				Kind:      "anthropic",
				Model:     "claude-sonnet-4-5",
				MaxTokens: 16000,
			},
		},
		defaultProviderName: "test",
	}
	// In production, buildManager reads the metadata into cfg once at
	// step-open time, then buildDefaultSpec reads from cfg. Mirror that
	// here so the assertion targets the same code path.
	if v, ok := stream.GetMetadata("workshop.thinking_level"); ok {
		pc := cfg.providers[cfg.defaultProviderName]
		pc.ThinkingLevel = v
		cfg.providers[cfg.defaultProviderName] = pc
	}

	spec := buildDefaultSpec(cfg.defaultProviderConfig())
	assert.Equal(t, models.ThinkingLevelHigh, spec.ThinkingLevel)
}

// TestBuildManager_CompactionProvider_DefaultsToInference verifies the
// "fall back to default" behavior: when CompactionConfig.Provider is
// empty, /compact uses the same provider as inference. A direct,
// low-level check: a config with two providers, default "sonnet" and
// an unset compaction.Provider, builds without error.
func TestBuildManager_CompactionProvider_DefaultsToInference(t *testing.T) {
	cfg := &config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"haiku":  {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
			"sonnet": {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
		},
		defaultProviderName: "sonnet",
		compaction: CompactionConfig{
			MaxTokens: 50000, // Provider is intentionally left empty.
		},
	}
	mgr, err := buildManager(cfg)
	if err != nil {
		t.Fatalf("buildManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

// TestBuildManager_CompactionProvider_DistinctFromInference verifies
// that compaction.provider can point at a *different* named provider
// than the inference default. The two are built and registered as
// separate provider.Provider instances, even though they share the
// same underlying SDK; buildManager must not silently alias them.
func TestBuildManager_CompactionProvider_DistinctFromInference(t *testing.T) {
	cfg := &config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"haiku":  {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
			"sonnet": {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
		},
		defaultProviderName: "sonnet",
		compaction: CompactionConfig{
			Provider:  "haiku",
			MaxTokens: 50000,
		},
	}
	mgr, err := buildManager(cfg)
	if err != nil {
		t.Fatalf("buildManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

// TestBuildManager_CompactionProvider_UndefinedErrors verifies the
// validation contract: a compaction.provider that references a name
// not in the providers map must fail at startup with a clear error
// message naming the undefined name and listing the defined ones.
func TestBuildManager_CompactionProvider_UndefinedErrors(t *testing.T) {
	cfg := &config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"haiku": {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
		},
		defaultProviderName: "haiku",
		compaction: CompactionConfig{
			Provider:  "nonexistent",
			MaxTokens: 50000,
		},
	}
	_, err := buildManager(cfg)
	if err == nil {
		t.Fatal("expected error for undefined compaction.provider")
	}
	if !strings.Contains(err.Error(), `compaction.provider "nonexistent" is not defined`) {
		t.Errorf("unexpected error message: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "haiku") {
		t.Errorf("error should list defined providers (haiku); got %q", err.Error())
	}
}

// TestBuildManager_CompactionZeroBudget verifies that a config with
// compaction.max-tokens = 0 builds cleanly. /compact is always
// available; the field is a pure per-call output budget (0 = use
// framework default 8192 in ore/compaction), not a kill switch.
func TestBuildManager_CompactionZeroBudget(t *testing.T) {
	cfg := &config{
		storeDir: t.TempDir(),
		providers: map[string]ProviderConfig{
			"haiku": {Kind: "openai", APIKey: "sk-test", Model: "test-model"},
		},
		defaultProviderName: "haiku",
		compaction: CompactionConfig{
			// Provider set; MaxTokens is 0 (use framework default).
			Provider:  "haiku",
			MaxTokens: 0,
		},
	}
	mgr, err := buildManager(cfg)
	if err != nil {
		t.Fatalf("buildManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("buildManager returned nil manager")
	}
}

// newAnalyticsCommandStream creates a fresh in-memory session manager
// and stream for the analytics-command tests. The provider is a no-op;
// only the slash handler is exercised.
func newAnalyticsCommandStream(t *testing.T) *session.Stream {
	t.Helper()
	store := session.NewMemoryStore()
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return st, nil
	})
	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	return stream
}

func TestAnalyticsCommand_NoStreamFriendlyMessage(t *testing.T) {
	// With no stream wired, the handler must surface the friendly
	// empty-state message rather than panicking. This is the
	// nil-stream-safe contract from the slash command design.
	ac := &analyticsCommand{}
	res, err := ac.Handler(context.Background(), nil, slash.Command{Name: "analytics", Input: ""})
	require.NoError(t, err)
	assert.Equal(t, "No artifacts in this thread yet.", res.Notice.Content)
}

func TestAnalyticsCommand_EmptyThreadFriendlyMessage(t *testing.T) {
	// A freshly-created thread has no turns yet. AnalyzeTurns returns
	// nil and Render translates that to the same friendly message.
	stream := newAnalyticsCommandStream(t)
	ac := &analyticsCommand{}
	ac.SetStream(stream)

	res, err := ac.Handler(context.Background(), nil, slash.Command{Name: "analytics", Input: ""})
	require.NoError(t, err)
	assert.Equal(t, "No artifacts in this thread yet.", res.Notice.Content)
}

func TestAnalyticsCommand_RendersTable(t *testing.T) {
	// A populated thread produces a Markdown table. We assert on the
	// structural shape rather than exact formatting so the test is
	// resilient to changes in the column layout.
	stream := newAnalyticsCommandStream(t)

	// Seed two turns: one with a single Text artifact, one with two.
	if err := stream.Process(context.Background(), session.UserMessageEvent{Content: "first"}); err != nil {
		t.Fatalf("process first turn: %v", err)
	}
	if err := stream.Process(context.Background(), session.UserMessageEvent{Content: "second turn"}); err != nil {
		t.Fatalf("process second turn: %v", err)
	}

	ac := &analyticsCommand{}
	ac.SetStream(stream)

	res, err := ac.Handler(context.Background(), nil, slash.Command{Name: "analytics", Input: ""})
	require.NoError(t, err)

	// Header + separator + at least one data row + totals row.
	assert.Contains(t, res.Notice.Content, "| Kind", "must include the header row")
	assert.Contains(t, res.Notice.Content, "|---", "must include the separator row")
	assert.Contains(t, res.Notice.Content, "**total**", "must include the bolded totals row")
}

func TestAnalyticsCommand_ConsumesEvent(t *testing.T) {
	// /analytics is slash-only by design: the event must be consumed
	// (Result.Replace is nil) so no LLM inference is triggered. The
	// slash registry uses Result.Replace to decide whether to feed
	// the event into the inference pipeline.
	stream := newAnalyticsCommandStream(t)
	ac := &analyticsCommand{}
	ac.SetStream(stream)

	res, err := ac.Handler(context.Background(), nil, slash.Command{Name: "analytics", Input: ""})
	require.NoError(t, err)
	assert.Nil(t, res.Replace, "analytics must consume the event (no LLM call)")
}
