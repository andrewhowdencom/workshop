// Package app provides the core TUI application logic for the workshop coding
// assistant. It wires together the ore framework's TUI conduit, system prompt
// transforms, guardrails, and tool registry to create an interactive coding agent.
package app

import (
	"context"
	"fmt"

	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/thread"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
)

// config holds the runtime configuration for the application.
type config struct {
	threadID string
	apiKey   string
	model    string
	baseURL  string
	storeDir string
}

// Option configures the application via functional options.
type Option func(*config)

// WithThreadID sets the thread UUID to resume an existing conversation.
func WithThreadID(id string) Option {
	return func(c *config) { c.threadID = id }
}

// WithAPIKey sets the OpenAI-compatible API key.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithModel sets the model name. Defaults to "gpt-4o" if not provided.
func WithModel(model string) Option {
	return func(c *config) { c.model = model }
}

// WithBaseURL sets a custom base URL for the API provider.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithStoreDir sets the directory for persistent JSON thread storage.
// If empty, an in-memory store is used.
func WithStoreDir(dir string) Option {
	return func(c *config) { c.storeDir = dir }
}

// Run initializes and starts the TUI application.
func Run(ctx context.Context, opts ...Option) error {
	cfg := &config{
		model: "gpt-4o",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.apiKey == "" {
		return fmt.Errorf("api key not set")
	}

	// Create thread store.
	var store thread.Store
	if cfg.storeDir != "" {
		var err error
		store, err = thread.NewJSONStore(cfg.storeDir)
		if err != nil {
			return fmt.Errorf("create JSON store: %w", err)
		}
	} else {
		store = thread.NewMemoryStore()
	}

	// Build OpenAI provider.
	var provOpts []openai.Option
	if cfg.baseURL != "" {
		provOpts = append(provOpts, openai.WithBaseURL(cfg.baseURL))
	}
	prov, err := openai.New(append([]openai.Option{
		openai.WithAPIKey(cfg.apiKey),
		openai.WithModel(cfg.model),
	}, provOpts...)...)
	if err != nil {
		return fmt.Errorf("create openai provider: %w", err)
	}

	// Step factory: inject system prompt and guardrails as transforms.
	stepFactory := func() (*loop.Step, error) {
		sp, err := systemprompt.New(systemprompt.WithContent(
			"You are a terminal-based coding assistant. " +
				"You help users write, review, refactor, and debug code across any language or framework. " +
				"You have access to filesystem tools (read_file, write_file, edit_file, list_directory, search_files) and a bash tool for running shell commands. " +
				"Use these tools proactively to explore the codebase, make changes, run tests, and verify your work. " +
				"Prefer concise explanations and actionable suggestions.",
		))
		if err != nil {
			return nil, fmt.Errorf("create system prompt transform: %w", err)
		}

		gr, err := guardrails.New(guardrails.WithRules(
			"Always format code in markdown blocks with the correct language tag.",
			"Prefer concise explanations; show code rather than prose where possible.",
			"When suggesting changes, explain the rationale briefly.",
			"Before writing or editing files, verify the target path and confirm the change is intended.",
		))
		if err != nil {
			return nil, fmt.Errorf("create guardrails transform: %w", err)
		}

		// Create tool registry with filesystem and bash functions.
		registry := tool.NewRegistry()
		registry.Register(filesystem.ReadFileTool.Name, filesystem.ReadFileTool.Description, filesystem.ReadFileTool.Schema, filesystem.ReadFile)
		registry.Register(filesystem.WriteFileTool.Name, filesystem.WriteFileTool.Description, filesystem.WriteFileTool.Schema, filesystem.WriteFile)
		registry.Register(filesystem.EditFileTool.Name, filesystem.EditFileTool.Description, filesystem.EditFileTool.Schema, filesystem.EditFile)
		registry.Register(filesystem.ListDirectoryTool.Name, filesystem.ListDirectoryTool.Description, filesystem.ListDirectoryTool.Schema, filesystem.ListDirectory)
		registry.Register(filesystem.SearchFilesTool.Name, filesystem.SearchFilesTool.Description, filesystem.SearchFilesTool.Schema, filesystem.SearchFiles)
		registry.Register(bash.BashTool.Name, bash.BashTool.Description, bash.BashTool.Schema, bash.Bash)

		return loop.New(
			loop.WithTransforms(sp, gr),
			loop.WithHandlers(registry.Handler()),
			loop.WithInvokeOptions(openai.WithTools(registry.Tools())),
		), nil
	}

	// Create session manager with the ReAct cognitive pattern.
	mgr := session.NewManager(store, prov, stepFactory, cognitive.NewTurnProcessor())

	// Create the TUI conduit, passing the thread ID via functional option.
	conduit, err := tui.New(mgr, tui.WithThreadID(cfg.threadID))
	if err != nil {
		return fmt.Errorf("create TUI conduit: %w", err)
	}

	// Start the TUI and block until the context is cancelled.
	return conduit.Start(ctx)
}
