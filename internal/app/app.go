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
		// Resolve the roles directory once for this step.
		rdir := roleDir()

		// Build dynamic system prompt that reads from thread metadata.
		currentPrompt := makeCurrentPrompt(rdir, thr)

		sp, err := systemprompt.New(systemprompt.WithContentFunc(currentPrompt))
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

		// Role management tools.
		registry.Register("list_roles", "List all available role definitions.", listRolesSchema, makeListRolesHandler(rdir))
		registry.Register("get_current_role", "Get the currently active role for this thread.", getCurrentRoleSchema, makeGetCurrentRoleHandler(rdir, thr))
		registry.Register("switch_role", "Switch to a different role for this thread.", switchRoleSchema, makeSwitchRoleHandler(rdir, thr))

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

// defaultPrompt is the baked-in system prompt used when no role is active.
const defaultPrompt = "You are a terminal-based coding assistant. " +
	"You help users write, review, refactor, and debug code across any language or framework. " +
	"You have access to filesystem tools (read_file, write_file, edit_file, list_directory, search_files) and a bash tool for running shell commands. " +
	"Use these tools proactively to explore the codebase, make changes, run tests, and verify your work. " +
	"Prefer concise explanations and actionable suggestions."

// makeCurrentPrompt returns a closure that reads the active role from thread
// metadata and returns the corresponding prompt, falling back to defaultPrompt.
func makeCurrentPrompt(rdir string, thr *thread.Thread) func() string {
	return func() string {
		if roleName, ok := thr.GetMetadata("workshop.role"); ok && roleName != "" {
			if role, err := loadRole(rdir, roleName); err == nil {
				return role.Prompt
			}
		}
		return defaultPrompt
	}
}

// makeListRolesHandler returns a tool handler that lists available role
// definitions from the given directory.
func makeListRolesHandler(rdir string) tool.ToolFunc {
	return func(ctx context.Context, args map[string]any) (any, error) {
		roles, err := listRoleDefinitions(rdir)
		if err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0, len(roles))
		for _, r := range roles {
			result = append(result, map[string]any{
				"name":        r.Name,
				"description": r.Description,
			})
		}
		return result, nil
	}
}

// makeGetCurrentRoleHandler returns a tool handler that returns the currently
// active role for the given thread.
func makeGetCurrentRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc {
	return func(ctx context.Context, args map[string]any) (any, error) {
		roleName := "default"
		if v, ok := thr.GetMetadata("workshop.role"); ok && v != "" {
			roleName = v
		}
		role, err := loadRole(rdir, roleName)
		if err != nil {
			return map[string]any{
				"role":           roleName,
				"description":    "",
				"prompt_preview": "",
			}, nil
		}
		preview := role.Prompt
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return map[string]any{
			"role":           role.Name,
			"description":    role.Description,
			"prompt_preview": preview,
		}, nil
	}
}

// makeSwitchRoleHandler returns a tool handler that validates and switches
// the active role for the given thread.
func makeSwitchRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc {
	return func(ctx context.Context, args map[string]any) (any, error) {
		name, ok := args["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("missing required argument: name")
		}
		if _, err := loadRole(rdir, name); err != nil {
			return nil, fmt.Errorf("role %q not found", name)
		}
		thr.SetMetadata("workshop.role", name)
		return fmt.Sprintf("Switched to role: %s", name), nil
	}
}
