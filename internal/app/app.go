// Package app provides the core application logic for the workshop coding
// assistant. It wires together the ore framework's TUI conduit, HTTP web UI
// conduit, system prompt transforms, guardrails, and tool registry to create
// an interactive coding agent.
//
// The system prompt is composed dynamically from three sources:
//
//  1. The active role definition (or a default prompt if none is set).
//  2. A contextual sentence describing the current working directory.
//  3. Repository-level instructions discovered by walking parent directories
//     from the working directory toward the root, collecting AGENTS.md and
//     CLAUDE.md files nearest-first.
package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/state"
	"github.com/andrewhowdencom/ore/tool"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/andrewhowdencom/ore/x/compaction"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
	stdioc "github.com/andrewhowdencom/ore/x/conduit/stdio"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	slash "github.com/andrewhowdencom/ore/x/slash"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	xtool "github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	"github.com/andrewhowdencom/ore/x/tool/settitle"
	"github.com/andrewhowdencom/ore/x/tool/skills"
	"github.com/andrewhowdencom/ore/x/usage"

	"github.com/adrg/xdg"
)

// ProviderConfig holds the user-supplied configuration for a concrete provider.
type ProviderConfig struct {
	Kind            string // e.g. "openai"
	APIKey          string
	Model           string
	BaseURL         string
	Temperature     float64
	ReasoningEffort string
}

// CompactionConfig holds the configuration for the state compaction
// framework that reduces conversation history before each inference turn.
type CompactionConfig struct {
	MaxTokens int // 0 = disabled
}

// config holds the runtime configuration for the application.
type config struct {
	threadID   string
	storeDir   string
	httpAddr   string
	provider   ProviderConfig
	compaction CompactionConfig
	workingDir string
	role       string
	tracer     trace.Tracer
	conduit    string // e.g. "TUI", "HTTP", "stdio"
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

// WithRole sets the initial role name for new threads.
func WithRole(name string) Option {
	return func(c *config) { c.role = name }
}

// WithCompaction sets the compaction configuration.
func WithCompaction(c CompactionConfig) Option {
	return func(cfg *config) { cfg.compaction = c }
}

// WithTracer sets the OpenTelemetry tracer for the application.
func WithTracer(tracer trace.Tracer) Option {
	return func(c *config) { c.tracer = tracer }
}

// RunTUI initializes and starts the TUI application.
func RunTUI(ctx context.Context, opts ...Option) error {
	cfg := &config{conduit: "TUI"}
	for _, opt := range opts {
		opt(cfg)
	}

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the TUI conduit.
	tuiConduit, err := tui.New(mgr,
		tui.WithThreadID(cfg.threadID),
		tui.WithName("ws"),
		tui.WithTracer(cfg.tracer),
		tui.WithStatusZones(map[string]string{
			"phase":      "lifecycle",
			"title":      "lifecycle",
			"thread_id":  "context",
			"cwd":        "context",
			"git_branch": "context",
			"role":       "context",
			"tui.pid":    "context",
			"model":      "context",
			"sent":       "lifecycle",
			"received":   "lifecycle",
			"total":      "lifecycle",
		}),
	)
	if err != nil {
		return fmt.Errorf("create TUI conduit: %w", err)
	}

	return tuiConduit.Start(ctx)
}

// RunHTTP initializes and starts the HTTP web UI application.
func RunHTTP(ctx context.Context, opts ...Option) error {
	cfg := &config{conduit: "HTTP"}
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
	httpConduit, err := httpc.New(mgr, httpc.WithUI(), httpc.WithName("workshop"), httpc.WithAddr(cfg.httpAddr), httpc.WithTracer(cfg.tracer))
	if err != nil {
		return fmt.Errorf("create HTTP conduit: %w", err)
	}

	return httpConduit.Start(ctx)
}

// RunStdio initializes and starts the stdio single-shot application.
func RunStdio(ctx context.Context, opts ...Option) error {
	cfg := &config{conduit: "stdio"}
	for _, opt := range opts {
		opt(cfg)
	}

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the stdio conduit.
	stdioConduit, err := stdioc.New(mgr, stdioc.WithThreadID(cfg.threadID), stdioc.WithTracer(cfg.tracer))
	if err != nil {
		return fmt.Errorf("create stdio conduit: %w", err)
	}

	return stdioConduit.Start(ctx)
}

// metadataReader is the minimal interface for reading thread metadata.
type metadataReader interface {
	GetMetadata(key string) (string, bool)
}

// metadataStore extends metadataReader with write access.
type metadataStore interface {
	metadataReader
	SetMetadata(key, value string)
}

// roleCommand handles the /role slash command for switching roles
// without triggering an LLM turn.
type roleCommand struct {
	mu     sync.Mutex
	stream *session.Stream
	rdir   string
}

// Handler validates the role name and updates the stream metadata.
func (c *roleCommand) Handler(ctx context.Context, cmd slash.Command) (slash.Result, error) {
	args := slash.Fields(cmd.Input)
	if len(args) == 0 {
		return slash.Result{}, fmt.Errorf("missing role name")
	}
	name := args[0]
	if _, err := loadRole(c.rdir, name, nil); err != nil {
		return slash.Result{}, fmt.Errorf("role %q not found: %w", name, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return slash.Result{}, fmt.Errorf("no active stream")
	}
	c.stream.SetMetadata("workshop.role", name)
	return slash.Result{}, nil
}

// SetStream updates the shared stream reference.
func (c *roleCommand) SetStream(s *session.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stream = s
}

// compactCommand handles the /compact slash command for forcing conversation
// compaction without triggering an LLM turn.
type compactCommand struct {
	mu        sync.Mutex
	stream    *session.Stream
	compactor *compaction.Compactor
}

// Handler forces an immediate compaction of the active thread's state.
// If compaction is disabled, it returns an error. The event is consumed
// (nil, nil) so no LLM inference is triggered.
func (c *compactCommand) Handler(ctx context.Context, cmd slash.Command) (slash.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.compactor == nil {
		return slash.Result{}, fmt.Errorf("compaction is not enabled")
	}
	if c.stream == nil {
		return slash.Result{}, fmt.Errorf("no active stream")
	}
	turns := c.stream.Turns()
	compacted, _, err := c.compactor.ForceCompact(ctx, turns)
	if err != nil {
		return slash.Result{}, err
	}
	c.stream.LoadTurns(compacted)
	if err := c.stream.Save(); err != nil {
		return slash.Result{}, fmt.Errorf("save thread: %w", err)
	}
	return slash.Result{}, nil
}

// SetStream updates the shared stream reference.
func (c *compactCommand) SetStream(s *session.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stream = s
}

// workshopSandbox is a FileSandbox that resolves relative paths against the
// active git worktree stored in stream metadata. Absolute paths pass through
// unchanged. It also provides WorkingDirectory for command execution defaults.
type workshopSandbox struct {
	name string
	mr   metadataReader
}

func (s *workshopSandbox) Name() string { return s.name }

func (s *workshopSandbox) ResolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if wtPath, ok := s.mr.GetMetadata("workshop.worktree.path"); ok && wtPath != "" {
		return filepath.Join(wtPath, path), nil
	}
	return path, nil
}

func (s *workshopSandbox) WorkingDirectory() string {
	if wtPath, ok := s.mr.GetMetadata("workshop.worktree.path"); ok && wtPath != "" {
		return wtPath
	}
	return ""
}

// buildManager creates the shared session manager from configuration.
func buildManager(cfg *config) (*session.Manager, error) {
	// Resolve tracer (noop fallback for tests that don't use WithTracer).
	tracer := cfg.tracer
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("")
	}

	// Create thread store.
	// Keep this fallback in sync with cmd/workshop/defaultStoreDir().
	storeDir := cfg.storeDir
	if storeDir == "" {
		storeDir = filepath.Join(xdg.DataHome, "workshop", "threads")
	}
	store, err := session.NewJSONStore(storeDir)
	if err != nil {
		return nil, fmt.Errorf("create JSON store: %w", err)
	}

	// Build provider from generic config.
	prov, err := newProvider(cfg.provider, tracer)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}

	// Build compactor if compaction is enabled.
	compactor := newCompactor(cfg.compaction, prov)

	// Create role command handler.
	rc := &roleCommand{rdir: roleDir()}

	// Create compact command handler.
	cc := &compactCommand{compactor: compactor}

	// Create slash command registry.
	slashReg := slash.NewRegistry()
	slashReg.Bind("role", "Switch to a different role", rc.Handler)
	slashReg.Bind("compact", "Compact conversation history", cc.Handler)

	// Step factory: inject system prompt and guardrails as transforms.
	stepFactory := func(stream *session.Stream) ([]loop.Option, error) {
		rc.SetStream(stream)
		cc.SetStream(stream)

		// Set up progressive skill discovery from repo and home directories.
		var discoverers []skills.Discoverer
		discoverers = append(discoverers, skills.NewFSDiscoverer(".agents/skills"))
		if homeDir, err := os.UserHomeDir(); err == nil {
			discoverers = append(discoverers, skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills")))
		}
		skillsToolkit := skills.NewToolkit(discoverers...)

		// Build the composable system prompt transform.
		sp, err := makeSystemPromptTransform(cfg, stream, skillsToolkit)
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

		// Register the workshop sandbox as the default. It resolves relative
		// paths against the active git worktree and provides the worktree
		// directory as the default working directory for command execution.
		if sbr, ok := registry.(tool.SandboxRegistry); ok {
			sbr.SetDefaultSandbox(&workshopSandbox{name: "workshop", mr: stream})
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

		// Workspace and git tools.
		mustRegisterRaw(registry, "workspace_create", "Create a new git worktree for isolated development.", createWorkspaceSchema, makeWorkspaceCreateHandler(stream))
		mustRegisterRaw(registry, "workspace_destroy", "Remove the git worktree created in this session.", destroyWorkspaceSchema, makeWorkspaceDestroyHandler(stream))
		mustRegisterRaw(registry, "git_commit", "Commit staged changes with automatic co-author attribution.", gitCommitSchema, makeGitCommitHandler(stream, cfg.provider))

		// Title management.
		mustRegisterRaw(registry, "settitle", "Set the conversation title visible to all conduits.", settitleSchema, settitle.Tool())

		invokeOpts := []provider.InvokeOption{openai.WithTools(registry.Tools())}
		if cfg.provider.Temperature != 0 {
			invokeOpts = append(invokeOpts, openai.WithTemperature(cfg.provider.Temperature))
		}
		if cfg.provider.ReasoningEffort != "" {
			invokeOpts = append(invokeOpts, openai.WithReasoningEffort(cfg.provider.ReasoningEffort))
		}

		return []loop.Option{
			loop.WithTransforms(sp, gr),
			loop.WithHandlers(xtool.NewHandler(registry, xtool.WithTracer(tracer)), usage.New()),
			loop.WithInvokeOptions(invokeOpts...),
			loop.WithTracer(tracer),
		}, nil
	}

	// Compute static metadata for all streams.
	cwd, _ := os.Getwd()
	shortCwd := cwd
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, home) {
		shortCwd = "~" + strings.TrimPrefix(cwd, home)
	}
	branchBytes, _ := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	branch := strings.TrimSpace(string(branchBytes))
	if branch == "" {
		branch = "(not in git repo)"
	}

	defaultMeta := func(stream *session.Stream) map[string]string {
		defaults := map[string]string{
			"thread_id":  stream.ID(),
			"cwd":        shortCwd,
			"git_branch": branch,
		}
		role := ""
		if r, ok := stream.GetMetadata("workshop.role"); ok {
			role = r
		} else if cfg.role != "" {
			role = cfg.role
			defaults["workshop.role"] = role
		}
		defaults["role"] = role
		defaults["tui.pid"] = strconv.Itoa(os.Getpid())
		return defaults
	}

	// Wrap the ReAct processor with optional compaction.
	processor := func(ctx context.Context, executor loop.TurnExecutor, st state.State, prov provider.Provider) (state.State, error) {
		if compactor != nil {
			if buf, ok := st.(*state.Buffer); ok {
				compacted, didCompact, err := compactor.MaybeCompact(ctx, buf.Turns())
				if err != nil {
					return nil, fmt.Errorf("compaction failed: %w", err)
				}
				if didCompact {
					buf.LoadTurns(compacted)
				}
			}
		}
		return cognitive.NewTurnProcessor(cognitive.ReActFactory, tracer)(ctx, executor, st, prov)
	}

	// Create session manager.
	return session.NewManager(store, prov, stepFactory, processor, session.WithDefaultMetadata(defaultMeta), session.WithInterceptor(slashReg)), nil
}

// makeSystemPromptTransform builds the composable system prompt transform for
// a given configuration and metadata reader. It concatenates four content sources:
//
//  1. The active role prompt (or defaultPrompt if no role is set).
//  2. A contextual sentence describing the current working directory.
//  3. The skills catalog fragment showing available skills to the LLM.
//  4. Repository-level instructions discovered by walking parent directories
//     from cfg.workingDir toward the root, collecting AGENTS.md and
//     CLAUDE.md files nearest-first.
//
// The resulting transform is passed to loop.Step via loop.WithTransforms.
func makeSystemPromptTransform(cfg *config, mr metadataReader, skillsToolkit *skills.Toolkit) (loop.Transform, error) {
	rdir := roleDir()
	currentPrompt := makeCurrentPrompt(rdir, mr)

	return systemprompt.New(
		systemprompt.WithContentFunc(currentPrompt),
		systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
		systemprompt.WithContextContentFunc(skillsToolkit.SystemPromptFragment()),
		systemprompt.WithContentFunc(source.AgentsMD(cfg.workingDir)),
		systemprompt.WithContentFunc(func() string {
			return fmt.Sprintf(
				"You are running the workshop agent (https://github.com/andrewhowdencom/workshop) on the %s conduit.",
				cfg.conduit,
			)
		}),
		systemprompt.WithContentFunc(func() string {
			if cfg.provider.Model == "" {
				return ""
			}
			return "You are running on model " + cfg.provider.Model + "."
		}),
		systemprompt.WithContentFunc(func() string {
			if cfg.provider.Kind == "" {
				return ""
			}
			return "Provider backend: " + cfg.provider.Kind
		}),
	)
}

// newProvider constructs a provider.Provider from generic ProviderConfig.
func newProvider(pc ProviderConfig, tracer trace.Tracer) (provider.Provider, error) {
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
		if tracer != nil {
			opts = append(opts, openai.WithTracer(tracer))
		}
		return openai.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported provider kind: %q", pc.Kind)
	}
}

// newCompactor builds a compactor from configuration. Returns nil if
// compaction is disabled (MaxTokens <= 0).
func newCompactor(cfg CompactionConfig, prov provider.Provider) *compaction.Compactor {
	if cfg.MaxTokens <= 0 {
		return nil
	}
	return compaction.New(
		compaction.WithTrigger(compaction.TokenUsageTrigger{MaxTokens: cfg.MaxTokens}),
		compaction.WithStrategy(compaction.SummarizeStrategy{
			Provider:  prov,
			MaxTokens: cfg.MaxTokens,
		}),
	)
}

// mustRegister panics if tool registration fails. Used for built-in tools
// whose schemas are baked-in and valid.
func mustRegister(registry tool.Registry, t tool.Tool, fn tool.ToolFunc) {
	if err := registry.Register(t, fn); err != nil {
		panic(fmt.Sprintf("register %s: %v", t.Name, err))
	}
}

// mustRegisterRaw is a convenience variant for tools that do not have a
// tool.Tool struct (e.g., ad-hoc role management tools).
func mustRegisterRaw(registry tool.Registry, name, description string, schema map[string]any, fn tool.ToolFunc) {
	if err := registry.Register(tool.Tool{Name: name, Description: description, Schema: schema}, fn); err != nil {
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
	"You also have access to workspace management tools (`workspace_create`, `workspace_destroy`) for isolated git worktrees, " +
	"and a `git_commit` tool that automatically appends co-author attribution.\n\n" +
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

// makeCurrentPrompt returns a closure that reads the active role from metadata
// and returns the corresponding prompt, falling back to defaultPrompt.
func makeCurrentPrompt(rdir string, mr metadataReader) func() string {
	return func() string {
		if roleName, ok := mr.GetMetadata("workshop.role"); ok && roleName != "" {
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

// makeWorkspaceCreateHandler returns a tool handler that creates a new git
// worktree under .worktrees/<branch> and stores its path in metadata.
func makeWorkspaceCreateHandler(ms metadataStore) tool.ToolFunc {
	return func(ctx context.Context, _ tool.Sandbox, args map[string]any) (any, error) {
		if existingPath, ok := ms.GetMetadata("workshop.worktree.path"); ok && existingPath != "" {
			return nil, fmt.Errorf("already inside worktree %q; nested worktrees are not allowed", existingPath)
		}

		branch, ok := args["branch"].(string)
		if !ok || branch == "" {
			return nil, fmt.Errorf("missing required argument: branch")
		}

		// Check if branch already exists.
		if err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branch).Run(); err == nil {
			return nil, fmt.Errorf("branch %q already exists", branch)
		}

		path := filepath.Join(".worktrees", branch)

		cmdArgs := []string{"worktree", "add", "-b", branch, path}
		if base, ok := args["base_branch"].(string); ok && base != "" {
			cmdArgs = append(cmdArgs, base)
		}

		cmd := exec.CommandContext(ctx, "git", cmdArgs...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git worktree add failed: %w\n%s", err, out.String())
		}

		ms.SetMetadata("workshop.worktree.path", path)
		return path, nil
	}
}

// makeWorkspaceDestroyHandler returns a tool handler that removes the worktree
// stored in metadata and clears the metadata key.
func makeWorkspaceDestroyHandler(ms metadataStore) tool.ToolFunc {
	return func(ctx context.Context, _ tool.Sandbox, args map[string]any) (any, error) {
		path, ok := ms.GetMetadata("workshop.worktree.path")
		if !ok || path == "" {
			return nil, fmt.Errorf("no worktree was created in this session")
		}

		cmd := exec.CommandContext(ctx, "git", "worktree", "remove", path)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git worktree remove failed: %w\n%s", err, out.String())
		}

		ms.SetMetadata("workshop.worktree.path", "")
		return fmt.Sprintf("Worktree %q removed", path), nil
	}
}

// makeGitCommitHandler returns a tool handler that commits staged changes with
// an automatic Co-Authored-By trailer derived from the provider config.
func makeGitCommitHandler(ms metadataStore, pc ProviderConfig) tool.ToolFunc {
	return func(ctx context.Context, sb tool.Sandbox, args map[string]any) (any, error) {
		title, ok := args["title"].(string)
		if !ok || strings.TrimSpace(title) == "" {
			return nil, fmt.Errorf("missing or empty required argument: title")
		}

		// Verify there are staged changes.
		diffCmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
		if fsb, ok := sb.(tool.FileSandbox); ok {
			if dir := fsb.WorkingDirectory(); dir != "" {
				diffCmd.Dir = dir
			}
		}
		if err := diffCmd.Run(); err == nil {
			return nil, fmt.Errorf("no staged changes to commit")
		}

		trailer := coAuthoredByTrailer(pc)
		msg := title
		if body, ok := args["message"].(string); ok && strings.TrimSpace(body) != "" {
			msg += "\n\n" + body
		}
		if trailer != "" {
			msg += "\n\n" + trailer
		}

		cmd := exec.CommandContext(ctx, "git", "commit", "-m", msg)
		if fsb, ok := sb.(tool.FileSandbox); ok {
			if dir := fsb.WorkingDirectory(); dir != "" {
				cmd.Dir = dir
			}
		}
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git commit failed: %w\n%s", err, out.String())
		}

		return out.String(), nil
	}
}

// coAuthoredByTrailer builds the Co-authored-by trailer from ProviderConfig.
// Format: Co-authored-by: <raw model> <stripped-model>@workshop.agent
func coAuthoredByTrailer(pc ProviderConfig) string {
	if pc.Model == "" || pc.Kind == "" {
		return ""
	}
	stripped := pc.Model
	if i := strings.LastIndex(stripped, "/"); i >= 0 {
		stripped = stripped[i+1:]
	}
	return fmt.Sprintf("Co-authored-by: %s <%s@workshop.agent>", pc.Model, stripped)
}
