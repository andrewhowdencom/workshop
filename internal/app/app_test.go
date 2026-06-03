package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/state"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	"github.com/andrewhowdencom/ore/x/tool/skills"
)

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

func TestMakeListRolesHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("Prompt A.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("Prompt B.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := makeListRolesHandler(dir)
	result, err := handler(context.Background(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	roles, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want []map[string]any", result)
	}
	if len(roles) != 2 {
		t.Fatalf("len(roles) = %d, want 2", len(roles))
	}
}

func TestMakeGetCurrentRoleHandler_Default(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	handler := makeGetCurrentRoleHandler(t.TempDir(), thr)
	result, err := handler(context.Background(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if m["role"] != "default" {
		t.Errorf("role = %q, want default", m["role"])
	}
}

func TestMakeGetCurrentRoleHandler_WithRole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "writer.md"), []byte("---\nname: writer\ndescription: W\n---\nYou write.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.role"] = "writer"

	handler := makeGetCurrentRoleHandler(dir, thr)
	result, err := handler(context.Background(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if m["role"] != "writer" {
		t.Errorf("role = %q, want writer", m["role"])
	}
	if m["description"] != "W" {
		t.Errorf("description = %q, want W", m["description"])
	}
}

func TestMakeSwitchRoleHandler_MissingName(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	handler := makeSwitchRoleHandler(t.TempDir(), thr)
	_, err = handler(context.Background(), nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

func TestMakeSwitchRoleHandler_InvalidRole(t *testing.T) {
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	handler := makeSwitchRoleHandler(t.TempDir(), thr)
	_, err = handler(context.Background(), nil, map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
}

func TestMakeSwitchRoleHandler_Success(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	handler := makeSwitchRoleHandler(dir, thr)
	result, err := handler(context.Background(), nil, map[string]any{"name": "reviewer"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	want := "Switched to role: reviewer"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}

	v, ok := thr.Metadata["workshop.role"]
	if !ok || v != "reviewer" {
		t.Errorf("metadata = %q, want reviewer", v)
	}
}

func TestMakeSwitchRoleHandler_FrontmatterNameMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planner.md"), []byte("---\nname: strategist\ndescription: Strategic planning role\n---\nYou are a strategic planner.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// list_roles should return the filename "planner", not the frontmatter "strategist"
	listHandler := makeListRolesHandler(dir)
	result, err := listHandler(context.Background(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("list handler error: %v", err)
	}

	roles, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want []map[string]any", result)
	}
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1", len(roles))
	}
	if roles[0]["name"] != "planner" {
		t.Errorf("role name = %q, want planner", roles[0]["name"])
	}

	// switch_role should succeed with "planner"
	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	switchHandler := makeSwitchRoleHandler(dir, thr)
	switchResult, err := switchHandler(context.Background(), nil, map[string]any{"name": "planner"})
	if err != nil {
		t.Fatalf("switch handler error: %v", err)
	}
	want := "Switched to role: planner"
	if switchResult != want {
		t.Errorf("switch result = %q, want %q", switchResult, want)
	}

	v, ok := thr.Metadata["workshop.role"]
	if !ok || v != "planner" {
		t.Errorf("metadata = %q, want planner", v)
	}
}

func TestRoleToolSchemas(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		checks func(t *testing.T, schema map[string]any)
	}{
		{
			name:   "listRolesSchema",
			schema: listRolesSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("listRolesSchema.type = %v, want object", schema["type"])
				}
			},
		},
		{
			name:   "getCurrentRoleSchema",
			schema: getCurrentRoleSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("getCurrentRoleSchema.type = %v, want object", schema["type"])
				}
			},
		},
		{
			name:   "switchRoleSchema",
			schema: switchRoleSchema,
			checks: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Errorf("switchRoleSchema.type = %v, want object", schema["type"])
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Fatal("switchRoleSchema missing properties")
				}
				nameProp, ok := props["name"].(map[string]any)
				if !ok {
					t.Fatal("switchRoleSchema.properties missing name")
				}
				if nameProp["type"] != "string" {
					t.Errorf("properties.name.type = %v, want string", nameProp["type"])
				}
				reqRaw, ok := schema["required"].([]interface{})
				if !ok {
					t.Fatalf("switchRoleSchema.required is not an array: %T", schema["required"])
				}
				found := false
				for _, r := range reqRaw {
					if s, ok := r.(string); ok && s == "name" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("required does not contain 'name': %v", reqRaw)
				}
			},
		},
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

	role, ok := stream.GetMetadata("workshop.role")
	if !ok {
		t.Fatal("workshop.role not seeded for new thread")
	}
	if role != "reviewer" {
		t.Errorf("workshop.role = %q, want reviewer", role)
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

	// Verify role was seeded
	if role, _ := stream1.GetMetadata("workshop.role"); role != "reviewer" {
		t.Fatalf("initial role = %q, want reviewer", role)
	}

	// Simulate role change during session (like switch_role tool)
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

func TestMakeListRolesHandler_SandboxPropagation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("Prompt A.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var resolveCalled bool
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			resolveCalled = true
			return path, nil
		},
	}

	handler := makeListRolesHandler(dir)
	_, err := handler(context.Background(), sb, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !resolveCalled {
		t.Error("handler did not pass sandbox to listRoleDefinitions")
	}
}

func TestMakeGetCurrentRoleHandler_SandboxPropagation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "writer.md"), []byte("---\nname: writer\ndescription: W\n---\nYou write.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.Metadata["workshop.role"] = "writer"

	var resolveCalled bool
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			resolveCalled = true
			return path, nil
		},
	}

	handler := makeGetCurrentRoleHandler(dir, thr)
	_, err = handler(context.Background(), sb, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !resolveCalled {
		t.Error("handler did not pass sandbox to loadRole")
	}
}

func TestMakeSwitchRoleHandler_SandboxPropagation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	var resolveCalled bool
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			resolveCalled = true
			return path, nil
		},
	}

	handler := makeSwitchRoleHandler(dir, thr)
	_, err = handler(context.Background(), sb, map[string]any{"name": "reviewer"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !resolveCalled {
		t.Error("handler did not pass sandbox to loadRole")
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
		"command":             "pwd",
		"working_directory":   explicitDir,
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
