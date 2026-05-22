package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewhowdencom/ore/thread"
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
	result, err := handler(context.Background(), map[string]any{})
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
	result, err := handler(context.Background(), map[string]any{})
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
	result, err := handler(context.Background(), map[string]any{})
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
	_, err = handler(context.Background(), map[string]any{})
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
	_, err = handler(context.Background(), map[string]any{"name": "nonexistent"})
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
	result, err := handler(context.Background(), map[string]any{"name": "reviewer"})
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
