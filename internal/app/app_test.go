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

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/state"
	slash "github.com/andrewhowdencom/ore/x/slash"
	"github.com/andrewhowdencom/ore/x/compaction"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	"github.com/andrewhowdencom/ore/x/tool/skills"
)

// keepLastN is a test-only compaction strategy that retains only the last N turns.
type keepLastN struct {
	N int
}

func (k keepLastN) Compact(ctx context.Context, turns []state.Turn) ([]state.Turn, error) {
	if len(turns) <= k.N {
		return turns, nil
	}
	return turns[len(turns)-k.N:], nil
}

func TestNewProvider_MissingAPIKey(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", Model: "gpt-4o"}
	_, err := newProvider(pc, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if err.Error() != "missing required provider config: api_key" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_MissingModel(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", APIKey: "sk-test"}
	_, err := newProvider(pc, nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if err.Error() != "missing required provider config: model" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_UnsupportedKind(t *testing.T) {
	pc := ProviderConfig{Kind: "unsupported", APIKey: "sk-test", Model: "gpt-4o"}
	_, err := newProvider(pc, nil)
	if err == nil {
		t.Fatal("expected error for unsupported provider kind")
	}
	want := `unsupported provider kind: "unsupported"`
	if err.Error() != want {
		t.Errorf("unexpected error message: %q, want %q", err.Error(), want)
	}
}

func TestMakeCurrentPrompt_Fallback(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	fn := makeCurrentPrompt(t.TempDir(), thr)
	got := fn()
	if got != defaultPrompt {
		t.Errorf("prompt = %q, want defaultPrompt", got)
	}
}

func TestDefaultPrompt_ContainsBehavioralDirective(t *testing.T) {
	if !strings.Contains(defaultPrompt, "When your task matches a skill description below") {
		t.Error("defaultPrompt missing behavioral directive for skills")
	}
}

func TestMakeCurrentPrompt_WithRole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("---\nname: reviewer\n---\nYou are a reviewer.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.role"] = "reviewer"

	fn := makeCurrentPrompt(dir, thr)
	got := fn()
	want := "You are a reviewer."
	if got != want {
		t.Errorf("prompt = %q, want %q", got, want)
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
	}, func(ctx context.Context, executor loop.TurnExecutor, st state.State, prov provider.Provider) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	rc := &roleCommand{rdir: dir}
	rc.SetStream(stream)

	// Valid role
	_, err = rc.Handler(context.Background(), slash.Command{Name: "role", Input: "reviewer"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	v, ok := stream.GetMetadata("workshop.role")
	if !ok || v != "reviewer" {
		t.Errorf("metadata = %q, want reviewer", v)
	}

	// Invalid role
	_, err = rc.Handler(context.Background(), slash.Command{Name: "role", Input: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}

	// Missing name
	_, err = rc.Handler(context.Background(), slash.Command{Name: "role", Input: ""})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestCompactSlashHandler_Disabled(t *testing.T) {
	store := session.NewMemoryStore()
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, executor loop.TurnExecutor, st state.State, prov provider.Provider) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	cc := &compactCommand{compactor: nil}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), slash.Command{Name: "compact", Input: ""})
	if err == nil {
		t.Fatal("expected error when compaction is disabled")
	}
	if !strings.Contains(err.Error(), "compaction is not enabled") {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestCompactSlashHandler_Enabled(t *testing.T) {
	store := session.NewMemoryStore()
	prov := &testSummarizeProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, executor loop.TurnExecutor, st state.State, prov provider.Provider) (state.State, error) {
		return st, nil
	})

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	// Pre-populate the stream with 5 user turns containing long text so
	// the heuristic token estimate exceeds MaxTokens=1.
	for i := 0; i < 5; i++ {
		err = stream.Process(context.Background(), session.UserMessageEvent{Content: strings.Repeat("a", 100)})
		if err != nil {
			t.Fatalf("process event %d: %v", i, err)
		}
	}

	turns := stream.Turns()
	if len(turns) != 5 {
		t.Fatalf("expected 5 turns, got %d", len(turns))
	}

	compactor := compaction.New(
		compaction.WithTrigger(compaction.TurnCountTrigger{N: 1}),
		compaction.WithStrategy(compaction.SummarizeStrategy{
			Provider: &testSummarizeProvider{},
		}),
	)
	cc := &compactCommand{compactor: compactor}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), slash.Command{Name: "compact", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got := stream.Turns()
	if len(got) != 1 {
		t.Fatalf("expected 1 turn after compaction, got %d", len(got))
	}
	if got[0].Role != state.RoleSystem {
		t.Errorf("first turn role = %v, want RoleSystem", got[0].Role)
	}
	if got[0].Artifacts[0].(artifact.Text).Content != "summary" {
		t.Errorf("summary turn = %q, want summary", got[0].Artifacts[0].(artifact.Text).Content)
	}
}

type testSlashProvider struct{}

func (p *testSlashProvider) Invoke(ctx context.Context, s state.State, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	return nil
}

type testSummarizeProvider struct{}

func (p *testSummarizeProvider) Invoke(ctx context.Context, s state.State, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	ch <- artifact.Text{Content: "summary"}
	return nil
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}

	stream, err := mgr.Create()
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	role, ok := stream.GetMetadata("role")
	if !ok {
		t.Fatal("role not seeded for new thread")
	}
	if role != "reviewer" {
		t.Errorf("role = %q, want reviewer", role)
	}
	// workshop.role should not be set for new threads; only explicit /role changes set it
	if _, ok := stream.GetMetadata("workshop.role"); ok {
		t.Error("workshop.role should not be seeded for new threads")
	}
}

func TestBuildManager_PreservesExistingRoleOnAttach(t *testing.T) {
	storeDir := t.TempDir()

	// First session: create with role "reviewer"
	mgr1, err := buildManager(&config{
		storeDir: storeDir,
		role:     "reviewer",
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
	})
	if err != nil {
		t.Fatalf("buildManager error: %v", err)
	}

	stream2, err := mgr2.Attach(threadID)
	if err != nil {
		t.Fatalf("attach stream: %v", err)
	}

	role, _ := stream2.GetMetadata("workshop.role")
	if role != "writer" {
		t.Errorf("workshop.role = %q, want writer (preserved from previous session)", role)
	}
	// The display role should reflect the persisted workshop.role, not the new cfg.role
	if displayRole, _ := stream2.GetMetadata("role"); displayRole != "writer" {
		t.Errorf("role = %q, want writer (from persisted workshop.role)", displayRole)
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
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: "/test/project",
		conduit:    "TUI",
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	rdir := t.TempDir()
	currentPrompt := makeCurrentPrompt(rdir, thr)

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
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: "",
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	rdir := t.TempDir()
	currentPrompt := makeCurrentPrompt(rdir, thr)

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

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: dir,
		conduit:    "TUI",
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	rdir := t.TempDir()
	currentPrompt := makeCurrentPrompt(rdir, thr)

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

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: child,
		conduit:    "TUI",
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	rdir := t.TempDir()
	currentPrompt := makeCurrentPrompt(rdir, thr)

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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit())
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit())
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
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test",
			Model:  "test-model",
		},
	}

	sp, err := makeSystemPromptTransform(cfg, thr, skills.NewToolkit())
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

	content, ok := result.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", result)
	}
	if !strings.Contains(content, "hello worktree") {
		t.Errorf("content = %q, want 'hello worktree'", content)
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

	content, ok := result.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", result)
	}
	if !strings.Contains(content, "outside content") {
		t.Errorf("content = %q, want 'outside content'", content)
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

	entries, ok := result.([]string)
	if !ok {
		t.Fatalf("result type = %T, want []string", result)
	}
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(entries))
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

	results, ok := result.([]filesystem.SearchResult)
	if !ok {
		t.Fatalf("result type = %T, want []filesystem.SearchResult", result)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
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

// invokedRecorder is a test double that records whether Invoke was called.
type invokedRecorder struct {
	invoked bool
}

func (m *invokedRecorder) Invoke(ctx context.Context, s state.State, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	m.invoked = true
	return nil
}

func TestNewCompactor_Disabled(t *testing.T) {
	compactor := newCompactor(CompactionConfig{MaxTokens: 0}, nil)
	if compactor != nil {
		t.Fatal("expected nil compactor when disabled")
	}
}

func TestNewCompactor_UsesSummarizeStrategy(t *testing.T) {
	mock := &invokedRecorder{}
	compactor := newCompactor(CompactionConfig{
		MaxTokens: 100,
	}, mock)

	if compactor == nil {
		t.Fatal("expected non-nil compactor")
	}

	// Trigger compaction: last turn has Usage exceeding MaxTokens,
	// and the heuristic token estimate exceeds MaxTokens so SummarizeStrategy invokes provider.
	turns := []state.Turn{
		{Role: state.RoleUser, Artifacts: []artifact.Artifact{artifact.Text{Content: strings.Repeat("a", 500)}}},
		{Role: state.RoleAssistant, Artifacts: []artifact.Artifact{
			artifact.Text{Content: "hi"},
			artifact.Usage{TotalTokens: 101},
		}},
	}

	_, didCompact, err := compactor.MaybeCompact(context.Background(), turns)
	if err != nil {
		t.Fatalf("MaybeCompact error: %v", err)
	}
	if !didCompact {
		t.Fatal("expected compaction to fire")
	}
	if !mock.invoked {
		t.Fatal("expected provider to be invoked (SummarizeStrategy calls provider; KeepLastN does not)")
	}
}

func TestCompactionNotifier(t *testing.T) {
	t.Run("NotifyWithoutReloader", func(t *testing.T) {
		n := &compactionNotifier{}
		n.Notify([]state.Turn{}) // should not panic
	})

	t.Run("NotifyWithReloader", func(t *testing.T) {
		n := &compactionNotifier{}
		var got []state.Turn
		n.SetReloader(func(turns []state.Turn) {
			got = turns
		})
		want := []state.Turn{{Role: state.RoleUser}}
		n.Notify(want)
		if len(got) != 1 || got[0].Role != state.RoleUser {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("SetReloaderOverwrites", func(t *testing.T) {
		n := &compactionNotifier{}
		var firstCalled, secondCalled bool
		n.SetReloader(func(turns []state.Turn) {
			firstCalled = true
		})
		n.SetReloader(func(turns []state.Turn) {
			secondCalled = true
		})
		n.Notify(nil)
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
		n.SetReloader(func(turns []state.Turn) {
			got = turns
		})
		n.Notify(nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("ThreadSafety", func(t *testing.T) {
		n := &compactionNotifier{}
		var count int64
		n.SetReloader(func(turns []state.Turn) {
			atomic.AddInt64(&count, 1)
		})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n.Notify([]state.Turn{})
			}()
		}
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				n.SetReloader(func(turns []state.Turn) {
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
	prov := &testSlashProvider{}
	mgr := session.NewManager(store, prov, func(stream *session.Stream) ([]loop.Option, error) {
		return nil, nil
	}, func(ctx context.Context, executor loop.TurnExecutor, st state.State, prov provider.Provider) (state.State, error) {
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

	compactor := compaction.New(
		compaction.WithStrategy(keepLastN{N: 2}),
	)

	var notified []state.Turn
	notifier := &compactionNotifier{}
	notifier.SetReloader(func(turns []state.Turn) {
		notified = turns
	})

	cc := &compactCommand{compactor: compactor, notifier: notifier}
	cc.SetStream(stream)

	_, err = cc.Handler(context.Background(), slash.Command{Name: "compact", Input: ""})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if len(notified) != 2 {
		t.Fatalf("expected notifier to receive 2 turns, got %d", len(notified))
	}
	if notified[0].Artifacts[0].(artifact.Text).Content != "message 3" {
		t.Errorf("first turn = %q, want message 3", notified[0].Artifacts[0].(artifact.Text).Content)
	}
	if notified[1].Artifacts[0].(artifact.Text).Content != "message 4" {
		t.Errorf("second turn = %q, want message 4", notified[1].Artifacts[0].(artifact.Text).Content)
	}
}

func TestBuildManager_CompactionNotifier(t *testing.T) {
	var notified []state.Turn
	notifier := &compactionNotifier{}
	notifier.SetReloader(func(turns []state.Turn) {
		notified = turns
	})

	mgr, err := buildManager(&config{
		storeDir: t.TempDir(),
		provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "sk-test-dummy",
			Model:  "test-model",
		},
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
	notifier.Notify(testTurns)
	if len(notified) != 1 || notified[0].Role != state.RoleUser {
		t.Errorf("notifier did not receive test turns: got %v", notified)
	}
}
