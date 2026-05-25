package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRole_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: reviewer
description: Code reviewer persona
---
You are a code reviewer. Focus on bugs.
`
	path := filepath.Join(dir, "reviewer.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	role, err := loadRole(dir, "reviewer", nil)
	if err != nil {
		t.Fatalf("loadRole error: %v", err)
	}

	if role.Name != "reviewer" {
		t.Errorf("Name = %q, want %q", role.Name, "reviewer")
	}
	if role.Description != "Code reviewer persona" {
		t.Errorf("Description = %q, want %q", role.Description, "Code reviewer persona")
	}
	wantPrompt := "You are a code reviewer. Focus on bugs."
	if role.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", role.Prompt, wantPrompt)
	}
}

func TestLoadRole_WithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := "You are a helpful assistant.\n"
	path := filepath.Join(dir, "default.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	role, err := loadRole(dir, "default", nil)
	if err != nil {
		t.Fatalf("loadRole error: %v", err)
	}

	if role.Name != "default" {
		t.Errorf("Name = %q, want %q", role.Name, "default")
	}
	if role.Description != "" {
		t.Errorf("Description = %q, want empty", role.Description)
	}
	wantPrompt := "You are a helpful assistant."
	if role.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", role.Prompt, wantPrompt)
	}
}

func TestLoadRole_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := loadRole(dir, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadRole_EmptyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := `---
---
Just a prompt.
`
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	role, err := loadRole(dir, "empty", nil)
	if err != nil {
		t.Fatalf("loadRole error: %v", err)
	}

	wantPrompt := "Just a prompt."
	if role.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", role.Prompt, wantPrompt)
	}
}

func TestLoadRole_FrontmatterNameDoesNotOverrideFilename(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: strategist
description: A strategic planner
---
You are a strategic planner.
`
	path := filepath.Join(dir, "planner.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	role, err := loadRole(dir, "planner", nil)
	if err != nil {
		t.Fatalf("loadRole error: %v", err)
	}

	if role.Name != "planner" {
		t.Errorf("Name = %q, want %q", role.Name, "planner")
	}
	if role.Description != "A strategic planner" {
		t.Errorf("Description = %q, want %q", role.Description, "A strategic planner")
	}
	wantPrompt := "You are a strategic planner."
	if role.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", role.Prompt, wantPrompt)
	}
}

func TestListRoleDefinitions_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"reviewer.md": "---\nname: reviewer\ndescription: R\n---\nPrompt R\n",
		"writer.md":   "---\nname: writer\ndescription: W\n---\nPrompt W\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	roles, err := listRoleDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("listRoleDefinitions error: %v", err)
	}

	if len(roles) != 2 {
		t.Fatalf("len(roles) = %d, want 2", len(roles))
	}
}

func TestListRoleDefinitions_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	roles, err := listRoleDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("listRoleDefinitions error: %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("len(roles) = %d, want 0", len(roles))
	}
}

func TestListRoleDefinitions_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("Good prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("Not a markdown.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	roles, err := listRoleDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("listRoleDefinitions error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1", len(roles))
	}
	if roles[0].Name != "good" {
		t.Errorf("role[0].Name = %q, want %q", roles[0].Name, "good")
	}
}

func TestListRoleDefinitions_SkipsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.md"), []byte("---\ninvalid: : yaml\n---\nPrompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "valid.md"), []byte("Valid prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	roles, err := listRoleDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("listRoleDefinitions error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1", len(roles))
	}
	if roles[0].Name != "valid" {
		t.Errorf("role[0].Name = %q, want %q", roles[0].Name, "valid")
	}
}

// mockFileSandbox is a test double that implements tool.FileSandbox.
type mockFileSandbox struct {
	resolveFunc func(string) (string, error)
	wd          string
}

func (m *mockFileSandbox) Name() string { return "mock" }

func (m *mockFileSandbox) ResolvePath(path string) (string, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(path)
	}
	return path, nil
}

func (m *mockFileSandbox) WorkingDirectory() string {
	if m.wd != "" {
		return m.wd
	}
	return "/mock"
}

func TestLoadRole_FileSandbox(t *testing.T) {
	dir := t.TempDir()
	content := "Prompt from resolved path.\n"
	resolvedPath := filepath.Join(dir, "resolved.md")
	if err := os.WriteFile(resolvedPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			if strings.HasSuffix(path, "original.md") {
				return resolvedPath, nil
			}
			return path, nil
		},
	}

	role, err := loadRole(dir, "original", sb)
	if err != nil {
		t.Fatalf("loadRole error: %v", err)
	}
	if role.Prompt != "Prompt from resolved path." {
		t.Errorf("Prompt = %q, want %q", role.Prompt, "Prompt from resolved path.")
	}
}

func TestLoadRole_FileSandboxError(t *testing.T) {
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			return "", fmt.Errorf("sandbox error")
		},
	}

	_, err := loadRole(t.TempDir(), "test", sb)
	if err == nil {
		t.Fatal("expected error for sandbox resolve failure")
	}
}

func TestListRoleDefinitions_FileSandbox(t *testing.T) {
	originalDir := t.TempDir()
	resolvedDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(resolvedDir, "role.md"), []byte("Prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			if path == originalDir {
				return resolvedDir, nil
			}
			return path, nil
		},
	}

	roles, err := listRoleDefinitions(originalDir, sb)
	if err != nil {
		t.Fatalf("listRoleDefinitions error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1", len(roles))
	}
	if roles[0].Name != "role" {
		t.Errorf("role[0].Name = %q, want %q", roles[0].Name, "role")
	}
}

func TestListRoleDefinitions_FileSandboxError(t *testing.T) {
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			return "", fmt.Errorf("sandbox error")
		},
	}

	_, err := listRoleDefinitions(t.TempDir(), sb)
	if err == nil {
		t.Fatal("expected error for sandbox resolve failure")
	}
}
