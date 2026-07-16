package app

// Sub-agent wiring for workshop. This file is intentionally separate
// from app.go so the stepFactory body remains small and the helpers
// can be unit-tested in isolation (subagent_test.go).
//
// The helpers here implement Path B from .plans/add-declarative-subagents.md:
// a fresh tool.Registry per sub-agent invocation, re-using the parent's
// (Tool, ToolFunc) closures so behavior (filesystem reads, bash runs,
// git_commit attribution) operates against the parent's stream.

import (
	"fmt"

	"github.com/andrewhowdencom/ore/agent"
	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/tool"
	osubagent "github.com/andrewhowdencom/ore/x/subagent"
	"github.com/andrewhowdencom/ore/x/provider/anthropic"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	xtool "github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	settitle "github.com/andrewhowdencom/ore/x/tool/set_title"

	"github.com/andrewhowdencom/workshop/internal/subagent"

	"go.opentelemetry.io/otel/trace"
)

// toolPair bundles a registered tool descriptor with its handler
// function so the sub-agent factory can wire the same closures into a
// fresh tool.Registry per call.
type toolPair struct {
	Tool tool.Tool
	Func tool.ToolFunc
}

// registerWorkshopTools wires the workshop's built-in tools into
// registry and returns the registered (Tool, ToolFunc) pairs indexed
// by tool name. The returned map is read by buildSubagentTool so the
// sub-agent's per-call registry advertises and handles the same tools
// the parent has.
//
// All tool handlers here close over `stream` (the parent's active
// conversation), so sub-agent tool calls see the same parent state:
// filesystem operations affect the parent's worktree, bash runs in
// the parent's working directory, git commits attribute via the
// parent's stream.
func registerWorkshopTools(
	registry tool.Registry,
	stream *junk.Stream,
	defaultProvider ProviderConfig,
) (map[string]toolPair, error) {
	pairs := make(map[string]toolPair)

	reg := func(t tool.Tool, fn tool.ToolFunc) error {
		if err := registry.Register(t, fn); err != nil {
			return fmt.Errorf("register %s: %w", t.Name, err)
		}
		pairs[t.Name] = toolPair{Tool: t, Func: fn}
		return nil
	}

	raw := func(name, description string, schema map[string]any, fn tool.ToolFunc) error {
		return reg(tool.Tool{Name: name, Description: description, Schema: schema}, fn)
	}

	// Filesystem built-ins.
	if err := reg(filesystem.ReadFileTool, filesystem.ReadFile); err != nil {
		return nil, err
	}
	if err := reg(filesystem.WriteFileTool, filesystem.WriteFile); err != nil {
		return nil, err
	}
	if err := reg(filesystem.EditFileTool, filesystem.EditFile); err != nil {
		return nil, err
	}
	if err := reg(filesystem.ListDirectoryTool, filesystem.ListDirectory); err != nil {
		return nil, err
	}
	if err := reg(filesystem.SearchFilesTool, filesystem.SearchFiles); err != nil {
		return nil, err
	}
	if err := reg(bash.BashTool, bash.Bash); err != nil {
		return nil, err
	}

	// Workshop-specific: workspace + git.
	if err := raw("workspace_create", "Create a new git worktree for isolated development.", createWorkspaceSchema, makeWorkspaceCreateHandler(stream)); err != nil {
		return nil, err
	}
	if err := raw("workspace_destroy", "Remove the git worktree created in this junk.", destroyWorkspaceSchema, makeWorkspaceDestroyHandler(stream)); err != nil {
		return nil, err
	}
	if err := raw("git_commit", "Commit staged changes with automatic co-author attribution.", gitCommitSchema, makeGitCommitHandler(stream, defaultProvider)); err != nil {
		return nil, err
	}

	// Title management.
	if err := raw("set_title", "Set the conversation title visible to all conduits.", setTitleSchema, settitle.Tool()); err != nil {
		return nil, err
	}

	return pairs, nil
}

// buildSubagentTool returns a (tool.Tool, tool.ToolFunc) pair for the
// given sub-agent definition. The returned closure constructs a fresh
// *agent.Agent per invocation, with cognitive.React as its pattern,
// the parent's provider and default spec, the JSON-result schema
// transform, a fresh sub-agent tool.Registry that re-uses the parent's
// (Tool, ToolFunc) pairs, and the same workshop sandbox as the parent.
//
// State isolation: agent.WithState is deliberately omitted so
// x/subagent.AsTool can seed a fresh ledger.Thread per call.
//
// providerKind mirrors cfg.providers[name].Kind (e.g., "anthropic" or
// "openai"), selecting the per-provider WithTools invoke option. The
// Kind is a string on ProviderConfig, not a method on the provider
// interface, so it is passed explicitly.
func buildSubagentTool(
	sa subagent.SubagentDefinition,
	prov provider.Provider,
	spec models.Spec,
	parentTools []tool.Tool,
	parentToolFuncs map[string]tool.ToolFunc,
	sandbox tool.Sandbox,
	tracer trace.Tracer,
	providerKind string,
) (tool.Tool, tool.ToolFunc) {
	factory := func() (*agent.Agent, error) {
		sp, err := osubagent.ResultSystemPrompt()
		if err != nil {
			return nil, fmt.Errorf("subagent %s: result system prompt: %w", sa.Name, err)
		}

		// Build a fresh sub-agent registry and re-register the parent's
		// tools with the same closures. Path B: shared closures, fresh
		// registry identity. The sandbox is shared so tool calls
		// resolve against the parent's stream.
		subReg := tool.NewRegistry()
		if sbr, ok := subReg.(tool.SandboxRegistry); ok {
			sbr.SetDefaultSandbox(sandbox)
		}
		for _, t := range parentTools {
			fn, ok := parentToolFuncs[t.Name]
			if !ok {
				continue
			}
			if err := subReg.Register(t, fn); err != nil {
				return nil, fmt.Errorf("subagent %s: register parent tool %s: %w", sa.Name, t.Name, err)
			}
		}

		// Wire per-provider invoke options so the LLM advertises
		// the tools. Mirrors buildInvokeOptions in app.go.
		var invokeOpts []provider.InvokeOption
		switch providerKind {
		case "anthropic":
			invokeOpts = append(invokeOpts, anthropic.WithTools(parentTools))
		default:
			invokeOpts = append(invokeOpts, openai.WithTools(parentTools))
		}

		return agent.New("subagent-"+sa.Name,
			agent.WithProvider(prov),
			agent.WithSpec(spec),
			agent.WithPattern(&cognitive.ReAct{}),
			agent.WithTransforms(sp),
			agent.WithHandlers(xtool.NewHandler(subReg, xtool.WithTracer(tracer))),
			agent.WithInvokeOptions(invokeOpts...),
			agent.WithTracer(tracer),
		), nil
	}

	return osubagent.AsTool(factory, sa.Name, sa.Description)
}
