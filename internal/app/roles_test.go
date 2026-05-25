package app

import (
	"os"
	"path/filepath"
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
