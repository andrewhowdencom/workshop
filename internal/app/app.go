// Package app provides the core application logic for the workshop coding
// assistant. It wires together the ore framework's TUI conduit, HTTP web UI
// conduit, system prompt transforms, guardrails, and tool registry to create
// an interactive coding agent.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/thread"
	"github.com/andrewhowdencom/ore/tool"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
	stdioc "github.com/andrewhowdencom/ore/x/conduit/stdio"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	xtool "github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	"github.com/andrewhowdencom/ore/x/tool/skills"
	unsandbox "github.com/andrewhowdencom/ore/x/tool/sandbox/unsafe"
	"github.com/adrg/xdg"
)

// ProviderConfig holds the user-supplied configuration for a concrete provider.
type ProviderConfig struct {
	Kind            string  // e.g. "openai"
	APIKey          string
	Model           string
	BaseURL         string
	Temperature     float64
	ReasoningEffort string
}

// config holds the runtime configuration for the application.
type config struct {
	threadID   string
	storeDir   string
	httpAddr   string
	provider   ProviderConfig
	workingDir string
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
// If empty, the default XDG data home path is used.
func WithStoreDir(dir string) Option {
	return func(c *config) { c.storeDir = dir }
}

// WithHTTPAddr sets the TCP address for the HTTP server (e.g. ":8080").
func WithHTTPAddr(addr string) Option {
	return func(c *config) { c.httpAddr = addr }
}

// WithWorkingDir sets the current working directory to include in the system prompt.
func WithWorkingDir(dir string) Option {
	return func(c *config) { c.workingDir = dir }
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

// RunStdio initializes and starts the stdio single-shot application.
func RunStdio(ctx context.Context, opts ...Option) error {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the stdio conduit.
	stdioConduit, err := stdioc.New(mgr, stdioc.WithThreadID(cfg.threadID))
	if err != nil {
		return fmt.Errorf("create stdio conduit: %w", err)
	}

	return stdioConduit.Start(ctx)
}

// buildManager creates the shared session manager from configuration.
func buildManager(cfg *config) (*session.Manager, error) {
	// Create thread store.
	storeDir := cfg.storeDir
	if storeDir == "" {
		storeDir = filepath.Join(xdg.DataHome, "workshop", "threads")
	}
	store, err := thread.NewJSONStore(storeDir)
	if err != nil {
		return nil, fmt.Errorf("create JSON store: %w", err)
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

		// Set up progressive skill discovery from repo and home directories.
		var discoverers []skills.Discoverer
		discoverers = append(discoverers, skills.NewFSDiscoverer(".agents/skills"))
		if homeDir, err := os.UserHomeDir(); err == nil {
			discoverers = append(discoverers, skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills")))
		}
		skillsToolkit := skills.NewToolkit(discoverers...)

		// Build dynamic system prompt that reads from thread metadata.
		currentPrompt := makeCurrentPrompt(rdir, thr)

		sp, err := systemprompt.New(
			systemprompt.WithContentFunc(currentPrompt),
			systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
			systemprompt.WithContextContentFunc(skillsToolkit.SystemPromptFragment()),
		)
		if err != nil {
			return nil, fmt.Errorf("create system prompt transform: %w", err)
		}

		gr, err := guardrails.New(guardrails.WithRules(
			"Always format code in markdown blocks with the correct language tag.",
			"Prefer concise explanations; show code rather than prose where possible.",
			"When suggesting changes, explain the rationale briefly.",
			"Before writing or editing files, verify the target path and confirm the change is intended.",
			"Before writing or editing files outside the current working directory, be especially cautious and confirm the change is intended.",
		))
		if err != nil {
			return nil, fmt.Errorf("create guardrails transform: %w", err)
		}

		// Create tool registry with filesystem and bash functions.
		registry := tool.NewRegistry()

		// Set the default sandbox to the unsafe sandbox. This satisfies the
		// ToolFunc contract so tools fall through to raw OS operations with no
		// isolation. The unsafe sandbox intentionally implements only
		// tool.Sandbox (Name), not FileSandbox or ExecSandbox, so path
		// resolution and command execution remain unconstrained.
		//
		// NOTE: This is deliberately insecure – it allows tools to execute raw
		// OS commands and access any filesystem path. Use ONLY for local
		// development. Swap for a secure sandbox (e.g., container-based)
		// before running untrusted code or in production.
		if sbr, ok := registry.(tool.SandboxRegistry); ok {
			sbr.SetDefaultSandbox(unsandbox.New("default"))
		}

		// Register skills toolkit tools into the registry.
		if err := skillsToolkit.Register(registry); err != nil {
			return nil, fmt.Errorf("register skills toolkit: %w", err)
		}

		mustRegister(registry, filesystem.ReadFileTool, filesystem.ReadFile)
		mustRegister(registry, filesystem.WriteFileTool, filesystem.WriteFile)
		mustRegister(registry, filesystem.EditFileTool, filesystem.EditFile)
		mustRegister(registry, filesystem.ListDirectoryTool, filesystem.ListDirectory)
		mustRegister(registry, filesystem.SearchFilesTool, filesystem.SearchFiles)
		mustRegister(registry, bash.BashTool, bash.Bash)

		// Role management tools.
		mustRegisterRaw(registry, "list_roles", "List all available role definitions.", listRolesSchema, makeListRolesHandler(rdir))
		mustRegisterRaw(registry, "get_current_role", "Get the currently active role for this thread.", getCurrentRoleSchema, makeGetCurrentRoleHandler(rdir, thr))
		mustRegisterRaw(registry, "switch_role", "Switch to a different role for this thread.", switchRoleSchema, makeSwitchRoleHandler(rdir, thr))

		invokeOpts := []provider.InvokeOption{openai.WithTools(registry.Tools())}
		if cfg.provider.Temperature != 0 {
			invokeOpts = append(invokeOpts, openai.WithTemperature(cfg.provider.Temperature))
		}
		if cfg.provider.ReasoningEffort != "" {
			invokeOpts = append(invokeOpts, openai.WithReasoningEffort(cfg.provider.ReasoningEffort))
		}

		return loop.New(
			loop.WithOnEmit(func(ctx context.Context, event loop.OutputEvent) {
				if tc, ok := event.(loop.TurnCompleteEvent); ok {
					thr.State.Append(tc.Turn.Role, tc.Turn.Artifacts...)
				}
			}),
			loop.WithTransforms(sp, gr),
			loop.WithHandlers(xtool.NewHandler(registry)),
			loop.WithInvokeOptions(invokeOpts...),
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

// mustRegister panics if tool registration fails. Used for built-in tools
// whose schemas are baked-in and valid.
func mustRegister(registry tool.Registry, t provider.Tool, fn tool.ToolFunc) {
	if err := registry.Register(t.Name, t.Description, t.Schema, fn); err != nil {
		panic(fmt.Sprintf("register %s: %v", t.Name, err))
	}
}

// mustRegisterRaw is a convenience variant for tools that do not have a
// provider.Tool struct (e.g., ad-hoc role management tools).
func mustRegisterRaw(registry tool.Registry, name, description string, schema map[string]any, fn tool.ToolFunc) {
	if err := registry.Register(name, description, schema, fn); err != nil {
		panic(fmt.Sprintf("register %s: %v", name, err))
	}
}

// defaultPrompt is the baked-in system prompt used when no role is active.
const defaultPrompt = "You are a terminal-based coding assistant. " +
	"You help users write, review, refactor, and debug code across any language or framework. " +
	"You have access to filesystem tools (read_file, write_file, edit_file, list_directory, search_files) and a bash tool for running shell commands. " +
	"When your task matches a skill description below, call read_skill to load its detailed instructions before proceeding. " +
	"Use these tools proactively to explore the codebase, make changes, run tests, and verify your work. " +
	"Prefer concise explanations and actionable suggestions.\n\n" +
	"# Engineering Intuition Defaults\n\n" +
	"When reasoning about code changes, default to these heuristics:\n\n" +
	"1. Simplicity is the highest good. If two approaches solve the same problem, choose the simpler one. " +
	"This principle overrides all others when they conflict.\n\n" +
	"2. Write all code as if it will be maintained for five years. Do not treat any change as temporary or throwaway. " +
	"Optimize for the long term, even when the immediate need seems small.\n\n" +
	"3. Refactoring is free. Do not avoid a better design because it requires more work. " +
	"Internal breaking changes are acceptable except for network APIs. Any refactoring must leave the system simpler.\n\n" +
	"4. Tests are the spec. Prioritize coverage over speed. Test-first by default. Run race detection (e.g. go test -race) to validate concurrency assumptions.\n\n" +
	"5. Fail fast. Surface errors immediately rather than swallowing or deferring them.\n\n" +
	"6. Explore proactively. Read full files, search the codebase, and understand context before making changes. Do not wait to be told.\n\n" +
	"7. Check git history before editing. Use git log and git blame to understand why code exists before changing it."

// makeCurrentPrompt returns a closure that reads the active role from thread
// metadata and returns the corresponding prompt, falling back to defaultPrompt.
func makeCurrentPrompt(rdir string, thr *thread.Thread) func() string {
	return func() string {
		if roleName, ok := thr.GetMetadata("workshop.role"); ok && roleName != "" {
			if role, err := loadRole(rdir, roleName, nil); err == nil {
				return role.Prompt
			}
		}
		return defaultPrompt
	}
}

// makeWorkingDirContent returns a closure that emits a sentence describing
// the current working directory, or an empty string if none is set.
func makeWorkingDirContent(dir string) func() string {
	return func() string {
		if dir == "" {
			return ""
		}
		return fmt.Sprintf("You are running in: %s. This is the user's active project directory; explore it proactively.", dir)
	}
}

// makeListRolesHandler returns a tool handler that lists available role
// definitions from the given directory. The sandbox argument is received
// from the tool framework and passed through to the role I/O layer.
func makeListRolesHandler(rdir string) tool.ToolFunc {
	return func(ctx context.Context, sb tool.Sandbox, args map[string]any) (any, error) {
		roles, err := listRoleDefinitions(rdir, sb)
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
// active role for the given thread. The sandbox argument is received from the
// tool framework and passed through to the role I/O layer.
func makeGetCurrentRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc {
	return func(ctx context.Context, sb tool.Sandbox, args map[string]any) (any, error) {
		roleName := "default"
		if v, ok := thr.GetMetadata("workshop.role"); ok && v != "" {
			roleName = v
		}
		role, err := loadRole(rdir, roleName, sb)
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
// the active role for the given thread. The sandbox argument is received
// from the tool framework and passed through to the role I/O layer.
func makeSwitchRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc {
	return func(ctx context.Context, sb tool.Sandbox, args map[string]any) (any, error) {
		name, ok := args["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("missing required argument: name")
		}
		if _, err := loadRole(rdir, name, sb); err != nil {
			return nil, fmt.Errorf("role %q not found", name)
		}
		thr.SetMetadata("workshop.role", name)
		return fmt.Sprintf("Switched to role: %s", name), nil
	}
}
