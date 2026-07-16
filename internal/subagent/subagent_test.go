package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSubagent_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := `---
description: A research-focused sub-agent
---
You are a research sub-agent. Find references and quote them.
`
	path := filepath.Join(dir, "researcher.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sa, err := LoadSubagent(dir, "researcher", nil)
	if err != nil {
		t.Fatalf("LoadSubagent error: %v", err)
	}

	if sa.Name != "researcher" {
		t.Errorf("Name = %q, want %q", sa.Name, "researcher")
	}
	if sa.Description != "A research-focused sub-agent" {
		t.Errorf("Description = %q, want %q", sa.Description, "A research-focused sub-agent")
	}
	wantPrompt := "You are a research sub-agent. Find references and quote them."
	if sa.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", sa.Prompt, wantPrompt)
	}
}

func TestLoadSubagent_WithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := "You are a focused assistant.\n"
	path := filepath.Join(dir, "default.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sa, err := LoadSubagent(dir, "default", nil)
	if err != nil {
		t.Fatalf("LoadSubagent error: %v", err)
	}

	if sa.Name != "default" {
		t.Errorf("Name = %q, want %q", sa.Name, "default")
	}
	if sa.Description != "" {
		t.Errorf("Description = %q, want empty", sa.Description)
	}
	wantPrompt := "You are a focused assistant."
	if sa.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", sa.Prompt, wantPrompt)
	}
}

func TestLoadSubagent_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSubagent(dir, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadSubagent_EmptyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := `---
---
Just a prompt.
`
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sa, err := LoadSubagent(dir, "empty", nil)
	if err != nil {
		t.Fatalf("LoadSubagent error: %v", err)
	}

	wantPrompt := "Just a prompt."
	if sa.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", sa.Prompt, wantPrompt)
	}
}

func TestLoadSubagent_FrontmatterNameDoesNotOverrideFilename(t *testing.T) {
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

	sa, err := LoadSubagent(dir, "planner", nil)
	if err != nil {
		t.Fatalf("LoadSubagent error: %v", err)
	}

	if sa.Name != "planner" {
		t.Errorf("Name = %q, want %q", sa.Name, "planner")
	}
	if sa.Description != "A strategic planner" {
		t.Errorf("Description = %q, want %q", sa.Description, "A strategic planner")
	}
	wantPrompt := "You are a strategic planner."
	if sa.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", sa.Prompt, wantPrompt)
	}
}

func TestListSubagentDefinitions_MultipleFiles(t *testing.T) {
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

	subs, err := ListSubagentDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("ListSubagentDefinitions error: %v", err)
	}

	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
}

func TestListSubagentDefinitions_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	subs, err := ListSubagentDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("ListSubagentDefinitions error: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("len(subs) = %d, want 0", len(subs))
	}
}

func TestListSubagentDefinitions_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("Good prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("Not a markdown.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	subs, err := ListSubagentDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("ListSubagentDefinitions error: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].Name != "good" {
		t.Errorf("subs[0].Name = %q, want %q", subs[0].Name, "good")
	}
}

func TestListSubagentDefinitions_SkipsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.md"), []byte("---\ninvalid: : yaml\n---\nPrompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "valid.md"), []byte("Valid prompt.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	subs, err := ListSubagentDefinitions(dir, nil)
	if err != nil {
		t.Fatalf("ListSubagentDefinitions error: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].Name != "valid" {
		t.Errorf("subs[0].Name = %q, want %q", subs[0].Name, "valid")
	}
}

// mockFileSandbox is a test double that implements tool.FileSandbox.
// Mirrors the same helper in internal/role/role_test.go.
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

func TestLoadSubagent_FileSandbox(t *testing.T) {
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

	sa, err := LoadSubagent(dir, "original", sb)
	if err != nil {
		t.Fatalf("LoadSubagent error: %v", err)
	}
	if sa.Prompt != "Prompt from resolved path." {
		t.Errorf("Prompt = %q, want %q", sa.Prompt, "Prompt from resolved path.")
	}
}

func TestLoadSubagent_FileSandboxError(t *testing.T) {
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			return "", fmt.Errorf("sandbox error")
		},
	}

	_, err := LoadSubagent(t.TempDir(), "test", sb)
	if err == nil {
		t.Fatal("expected error for sandbox resolve failure")
	}
}

func TestListSubagentDefinitions_FileSandbox(t *testing.T) {
	originalDir := t.TempDir()
	resolvedDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(resolvedDir, "sa.md"), []byte("Prompt.\n"), 0644); err != nil {
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

	subs, err := ListSubagentDefinitions(originalDir, sb)
	if err != nil {
		t.Fatalf("ListSubagentDefinitions error: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].Name != "sa" {
		t.Errorf("subs[0].Name = %q, want %q", subs[0].Name, "sa")
	}
}

func TestListSubagentDefinitions_FileSandboxError(t *testing.T) {
	sb := &mockFileSandbox{
		resolveFunc: func(path string) (string, error) {
			return "", fmt.Errorf("sandbox error")
		},
	}

	_, err := ListSubagentDefinitions(t.TempDir(), sb)
	if err == nil {
		t.Fatal("expected error for sandbox resolve failure")
	}
}

func TestExtractBody(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantBody        string
		wantFrontmatter string
	}{
		{
			name:            "full frontmatter and body",
			input:           "---\ndescription: foo\n---\nbody content\n",
			wantBody:        "body content",
			wantFrontmatter: "description: foo",
		},
		{
			name:            "no leading delimiter",
			input:           "no frontmatter here\n",
			wantBody:        "no frontmatter here",
			wantFrontmatter: "",
		},
		{
			name:            "unclosed frontmatter treated as body",
			input:           "---\ndescription: foo\nmore content\n",
			wantBody:        "---\ndescription: foo\nmore content",
			wantFrontmatter: "",
		},
		{
			name:            "empty body after delimiter",
			input:           "---\ndescription: foo\n---\n",
			wantBody:        "",
			wantFrontmatter: "description: foo",
		},
		{
			name:            "blank line between frontmatter and body",
			input:           "---\nkey: val\n---\n\nactual body\n",
			wantBody:        "actual body",
			wantFrontmatter: "key: val",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, frontmatter := ExtractBody(tt.input)
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
			if frontmatter != tt.wantFrontmatter {
				t.Errorf("frontmatter = %q, want %q", frontmatter, tt.wantFrontmatter)
			}
		})
	}
}

func TestLoadBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.md")
	if err := os.WriteFile(path, []byte("---\nname: reviewer\n---\nYou are a reviewer.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadBody(path, nil)
	if err != nil {
		t.Fatalf("LoadBody error: %v", err)
	}
	if got != "You are a reviewer." {
		t.Errorf("LoadBody = %q, want %q", got, "You are a reviewer.")
	}
}

func TestLoadBody_MissingFile(t *testing.T) {
	_, err := LoadBody(filepath.Join(t.TempDir(), "missing.md"), nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}