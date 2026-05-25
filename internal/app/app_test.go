package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/state"
	"github.com/andrewhowdencom/ore/thread"
	"github.com/andrewhowdencom/ore/x/systemprompt"
)

func TestNewProvider_MissingAPIKey(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", Model: "gpt-4o"}
	_, err := newProvider(pc)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if err.Error() != "missing required provider config: api_key" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_MissingModel(t *testing.T) {
	pc := ProviderConfig{Kind: "openai", APIKey: "sk-test"}
	_, err := newProvider(pc)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if err.Error() != "missing required provider config: model" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestNewProvider_UnsupportedKind(t *testing.T) {
	pc := ProviderConfig{Kind: "unsupported", APIKey: "sk-test", Model: "gpt-4o"}
	_, err := newProvider(pc)
	if err == nil {
		t.Fatal("expected error for unsupported provider kind")
	}
	want := `unsupported provider kind: "unsupported"`
	if err.Error() != want {
		t.Errorf("unexpected error message: %q, want %q", err.Error(), want)
	}
}

func TestMakeCurrentPrompt_Fallback(t *testing.T) {
	store := thread.NewMemoryStore()
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

func TestMakeCurrentPrompt_WithRole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("---\nname: reviewer\n---\nYou are a reviewer.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := thread.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.SetMetadata("workshop.role", "reviewer")

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
	store := thread.NewMemoryStore()
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

	store := thread.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	thr.SetMetadata("workshop.role", "writer")

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
	store := thread.NewMemoryStore()
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
	store := thread.NewMemoryStore()
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

	store := thread.NewMemoryStore()
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

	v, ok := thr.GetMetadata("workshop.role")
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
	store := thread.NewMemoryStore()
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

	v, ok := thr.GetMetadata("workshop.role")
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
	mgr, err := buildManager(&config{
		workingDir: "/test/project",
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

func TestSystemPrompt_WithCWD(t *testing.T) {
	store := thread.NewMemoryStore()
	thr, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		workingDir: "/test/project",
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
	store := thread.NewMemoryStore()
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
