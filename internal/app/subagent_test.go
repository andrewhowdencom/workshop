package app

// Tests for the sub-agent wiring introduced in subagent.go. Scope
// per .plans/add-declarative-subagents.md (Task 4, recommendation b):
// "registered tools have the right metadata" — the LLM round-trip is
// exercised manually and in future E2E suites.

import (
	"context"
	"strings"
	"testing"

	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/tool"

	"github.com/andrewhowdencom/workshop/internal/subagent"

	"go.opentelemetry.io/otel/trace/noop"
)

func TestBuildSubagentTool_RegistersMetadataFromDefinition(t *testing.T) {
	sa := subagent.SubagentDefinition{
		Name:        "researcher",
		Description: "Research-focused sub-agent.",
		Prompt:      "You are a researcher.",
	}

	tracer := noop.NewTracerProvider().Tracer("")
	var prov provider.Provider // interface-nil; factory not invoked

	saTool, _ := buildSubagentTool(sa, prov, models.Spec{}, nil, nil, nil, tracer, "openai")

	if saTool.Name != "researcher" {
		t.Errorf("Name = %q, want %q", saTool.Name, "researcher")
	}
	if saTool.Description != "Research-focused sub-agent." {
		t.Errorf("Description = %q, want %q", saTool.Description, "Research-focused sub-agent.")
	}
	if saTool.Schema == nil {
		t.Fatal("Schema is nil; expected subagent promptSchema")
	}
	props, ok := saTool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema[properties] is not a map: %T", saTool.Schema["properties"])
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("Schema.properties missing 'prompt' key")
	}
	if required, ok := saTool.Schema["required"].([]string); !ok || len(required) == 0 || required[0] != "prompt" {
		t.Errorf("Schema.required = %v, want [prompt]", required)
	}
}

func TestBuildSubagentTool_ToolFuncRejectsEmptyPrompt(t *testing.T) {
	sa := subagent.SubagentDefinition{Name: "researcher", Description: "d", Prompt: "p"}
	tracer := noop.NewTracerProvider().Tracer("")
	var prov provider.Provider

	_, saFn := buildSubagentTool(sa, prov, models.Spec{}, nil, nil, nil, tracer, "openai")

	_, err := saFn(context.Background(), nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error when prompt is missing; got nil")
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("error %q does not contain 'prompt is required'", err.Error())
	}
}

func TestRegisterWorkshopTools_RegistersExpectedTools(t *testing.T) {
	// Use a fresh registry; registerWorkshopTools wires the built-ins.
	// We don't pass a stream because the sandbox-resolved handlers
	// (workspace/git) accept nil — they'll error at call time, not
	// at registration time. The map keys are what we assert here.
	registry := tool.NewRegistry()

	pairs, err := registerWorkshopTools(registry, nil, ProviderConfig{})
	if err != nil {
		t.Fatalf("registerWorkshopTools error: %v", err)
	}

	wantTools := []string{
		"read_file", "write_file", "edit_file",
		"list_directory", "search_files", "bash",
		"workspace_create", "workspace_destroy", "git_commit",
		"set_title",
	}
	for _, name := range wantTools {
		if _, ok := pairs[name]; !ok {
			t.Errorf("pairs[%q] missing", name)
		}
	}
	if got := len(pairs); got != len(wantTools) {
		t.Errorf("len(pairs) = %d, want %d", got, len(wantTools))
	}

	// The same tools must be visible on the registry's exposed list
	// (registration must actually have happened, not just populated
	// the local pairs map).
	tools := registry.Tools()
	gotNames := make(map[string]bool, len(tools))
	for _, t := range tools {
		gotNames[t.Name] = true
	}
	for _, name := range wantTools {
		if !gotNames[name] {
			t.Errorf("registry.Tools() missing %q", name)
		}
	}
}

func TestRegisterWorkshopTools_OverwritesPriorRegistration(t *testing.T) {
	// The tool.Registry allows overwrites silently. Confirm that
	// registerWorkshopTools is consistent: if a caller pre-registers
	// a name the helper uses, the helper's registration overwrites
	// without error. The helper's callers (stepFactory) are
	// responsible for guarding against semantic collisions before
	// invoking it.
	registry := tool.NewRegistry()
	dummy := func(context.Context, tool.Sandbox, map[string]any) (any, error) { return "dummy", nil }
	if err := registry.Register(tool.Tool{Name: "read_file", Description: "preexisting"}, dummy); err != nil {
		t.Fatalf("seed Register: %v", err)
	}

	pairs, err := registerWorkshopTools(registry, nil, ProviderConfig{})
	if err != nil {
		t.Fatalf("registerWorkshopTools returned an error: %v", err)
	}
	if pairs["read_file"].Func == nil {
		t.Fatal("read_file func is nil after overwrite")
	}

	// The pre-existing tool's description should have been replaced
	// by the helper's filesystem.ReadFileTool.Description.
	tools := registry.Tools()
	for _, tl := range tools {
		if tl.Name != "read_file" {
			continue
		}
		if tl.Description == "preexisting" {
			t.Error("registry.Tools() still shows pre-existing read_file; helper did not overwrite")
		}
	}
}
