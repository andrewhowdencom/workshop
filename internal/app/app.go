// Package app provides the core application logic for the workshop coding
// assistant. It wires together the ore framework's TUI conduit, HTTP web UI
// conduit, system prompt transforms, guardrails, and tool registry to create
// an interactive coding agent.
package app

import (
	"context"
	"fmt"

	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/thread"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
)

// ProviderConfig holds the user-supplied configuration for a concrete provider.
type ProviderConfig struct {
	Kind    string // e.g. "openai"
	APIKey  string
	Model   string
	BaseURL string
}

// config holds the runtime configuration for the application.
type config struct {
	threadID string
	storeDir string
	httpAddr string
	provider ProviderConfig
}

// Option configures the application via functional options.
type Option func(*config)

// WithThreadID sets the thread UUID to resume an existing conversation.
func WithThreadID(id string) Option {
	return func(c *config) { c.threadID = id }
}

// WithProvider sets the provider configuration.
func WithProvider(p ProviderConfig) Option {
	return func(c *config) { c.provider = p }
}

// WithStoreDir sets the directory for persistent JSON thread storage.
// If empty, an in-memory store is used.
func WithStoreDir(dir string) Option {
	return func(c *config) { c.storeDir = dir }
}

// WithHTTPAddr sets the TCP address for the HTTP server (e.g. ":8080").
func WithHTTPAddr(addr string) Option {
	return func(c *config) { c.httpAddr = addr }
}

// RunTUI initializes and starts the TUI application.
func RunTUI(ctx context.Context, opts ...Option) error {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the TUI conduit.
	tuiConduit, err := tui.New(mgr, tui.WithThreadID(cfg.threadID))
	if err != nil {
		return fmt.Errorf("create TUI conduit: %w", err)
	}

	return tuiConduit.Start(ctx)
}

// RunHTTP initializes and starts the HTTP web UI application.
func RunHTTP(ctx context.Context, opts ...Option) error {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.httpAddr == "" {
		cfg.httpAddr = ":8080"
	}

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the HTTP conduit with web UI enabled.
	httpConduit, err := httpc.New(mgr, httpc.WithUI(), httpc.WithAddr(cfg.httpAddr))
	if err != nil {
		return fmt.Errorf("create HTTP conduit: %w", err)
	}

	return httpConduit.Start(ctx)
}

// buildManager creates the shared session manager from configuration.
func buildManager(cfg *config) (*session.Manager, error) {
	// Create thread store.
	var store thread.Store
	if cfg.storeDir != "" {
		var err error
		store, err = thread.NewJSONStore(cfg.storeDir)
		if err != nil {
			return nil, fmt.Errorf("create JSON store: %w", err)
		}
	} else {
		store = thread.NewMemoryStore()
	}

	// Build provider from generic config.
	prov, err := newProvider(cfg.provider)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}

	// Step factory: inject system prompt and guardrails as transforms.
	stepFactory := func(thr *thread.Thread) (*loop.Step, error) {
		_ = thr // reserved for future per-thread configuration

		sp, err := systemprompt.New(systemprompt.WithContentFunc(func() string {
			return "You are a terminal-based coding assistant. " +
				"You help users write, review, refactor, and debug code across any language or framework. " +
				"You have access to filesystem tools (read_file, write_file, edit_file, list_directory, search_files) and a bash tool for running shell commands. " +
				"Use these tools proactively to explore the codebase, make changes, run tests, and verify your work. " +
				"Prefer concise explanations and actionable suggestions."
		}))
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
	return session.NewManager(store, prov, stepFactory, cognitive.NewTurnProcessor()), nil
}

// newProvider constructs a provider.Provider from generic ProviderConfig.
func newProvider(pc ProviderConfig) (provider.Provider, error) {
	switch pc.Kind {
	case "", "openai":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("missing required provider config: api_key")
		}
		if pc.Model == "" {
			return nil, fmt.Errorf("missing required provider config: model")
		}
		var opts []openai.Option
		opts = append(opts, openai.WithAPIKey(pc.APIKey), openai.WithModel(pc.Model))
		if pc.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.BaseURL))
		}
		return openai.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported provider kind: %q", pc.Kind)
	}
}
