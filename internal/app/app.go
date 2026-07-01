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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewhowdencom/ore/agent"
	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/junk"
	state "github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/tool"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/andrewhowdencom/ore/x/analytics"
	"github.com/andrewhowdencom/ore/x/compaction"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
	stdioc "github.com/andrewhowdencom/ore/x/conduit/stdio"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/anthropic"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	"github.com/andrewhowdencom/ore/x/provider/retry"
	slash "github.com/andrewhowdencom/ore/x/slash"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	"github.com/andrewhowdencom/ore/x/telemetry"
	xtool "github.com/andrewhowdencom/ore/x/tool"
	"github.com/andrewhowdencom/ore/x/tool/bash"
	"github.com/andrewhowdencom/ore/x/tool/filesystem"
	settitle "github.com/andrewhowdencom/ore/x/tool/set_title"
	"github.com/andrewhowdencom/ore/x/tool/skills"
	"github.com/andrewhowdencom/ore/x/usage"

	"github.com/adrg/xdg"

	"github.com/andrewhowdencom/workshop/internal/role"
)

// ProviderConfig holds the user-supplied configuration for a concrete provider.
type ProviderConfig struct {
	Kind        string // e.g. "openai"
	APIKey      string
	Model       string
	BaseURL     string
	Temperature float64
	// ThinkingLevel is the qualitative reasoning effort. "off" disables
	// extended thinking entirely. The non-off levels (minimal, low,
	// medium, high, max) are translated to provider-specific parameters
	// at request time: percentage of max_tokens for Anthropic's
	// thinking.budget_tokens, or OpenAI's reasoning_effort vocabulary
	// (low | medium | high) for OpenAI-compatible providers. The empty
	// string is treated as "off". Default: "off".
	ThinkingLevel string
	// MaxTokens is the per-request output token cap forwarded to the
	// provider as models.Spec.MaxOutputTokens. Required by the
	// Anthropic provider (set to 0 to apply the workshop default of
	// 32000, applied at spec-build time); accepted but optional for
	// OpenAI-compatible providers.
	//
	// Note: distinct from CompactionConfig.MaxTokens, which (in the
	// ore v0.12 explicit-only compaction model) is the per-invocation
	// output budget for compaction.Summarize, not a request cap.
	MaxTokens int64
}

// CompactionConfig holds the configuration for the /compact slash
// command. In ore v0.12 compaction is explicit-only: there is no
// automatic pre-turn trigger. The /compact command calls
// compaction.Summarize and appends the result to the buffer.
type CompactionConfig struct {
	// Provider is the name of the named provider to use for the
	// compaction call. When empty, the command reuses the default
	// (inference) provider. When set, it must reference a key in the
	// `providers:` map; an undefined name errors at startup.
	Provider string
	// MaxTokens is the per-invocation output-token budget forwarded to
	// compaction.Summarize via models.Spec.MaxOutputTokens. When <= 0
	// the ore/compaction framework's default (8192) applies. /compact
	// is always available when a provider is configured; the field is
	// a pure budget, never a kill switch. In the previous API this
	// field was a trigger threshold (auto-compact at N tokens); that
	// semantic is gone with the move to explicit-only compaction.
	MaxTokens int
}

// compactionNotifier is a thread-safe callback bridge that forwards compacted
// turns (and the boundary info for the new collapse marker) to a registered
// reloader (e.g. the TUI conduit's ReloadHistory). The boundary is the
// BoundaryInfo returned by compaction.Summarize for the just-appended summary
// turn; pass the zero value when no compaction has occurred (the TUI renders
// no collapse marker in that case).
type compactionNotifier struct {
	mu       sync.Mutex
	reloader func(turns []state.Turn, boundary compaction.BoundaryInfo)
}

// SetReloader registers the callback that receives compacted turns.
func (n *compactionNotifier) SetReloader(fn func(turns []state.Turn, boundary compaction.BoundaryInfo)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reloader = fn
}

// Notify forwards the compacted turns (and boundary) to the registered reloader
// if any.
func (n *compactionNotifier) Notify(turns []state.Turn, boundary compaction.BoundaryInfo) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.reloader != nil {
		n.reloader(turns, boundary)
	}
}

// config holds the runtime configuration for the application.
type config struct {
	threadID  string
	storeDir  string
	httpAddr  string
	providers map[string]ProviderConfig
	// defaultProviderName is the name of the provider used for inference
	// (the main loop, the system prompt, the git_commit trailer, etc.).
	// It must reference a key in providers. Compaction has its own
	// reference (CompactionConfig.Provider); if that is empty, compaction
	// reuses the default.
	defaultProviderName string
	compaction          CompactionConfig
	workingDir          string
	role                string
	tracer              trace.Tracer
	meter               metric.Meter
	conduit             string // e.g. "TUI", "HTTP", "stdio"

	compactionNotifier *compactionNotifier
}

// defaultProviderConfig returns the ProviderConfig of the inference
// provider. Callers are expected to have already validated that
// defaultProviderName references a defined name; this helper does not
// defensively check. A missing default panics so the failure mode is
// loud and the call site is obvious.
func (c *config) defaultProviderConfig() ProviderConfig {
	return c.providers[c.defaultProviderName]
}

// Option configures the application via functional options.
type Option func(*config)

// WithThreadID sets the thread UUID to resume an existing conversation.
func WithThreadID(id string) Option {
	return func(c *config) { c.threadID = id }
}

// WithProvider registers a named provider under the given name. The
// same name can be used as the default (see WithDefaultProviderName)
// or as the compaction provider (see CompactionConfig.Provider).
func WithProvider(name string, p ProviderConfig) Option {
	return func(c *config) {
		if c.providers == nil {
			c.providers = make(map[string]ProviderConfig)
		}
		c.providers[name] = p
	}
}

// WithDefaultProviderName sets the name of the provider used for
// inference. The name must reference a key registered via
// WithProvider; validation happens in buildManager.
func WithDefaultProviderName(name string) Option {
	return func(c *config) { c.defaultProviderName = name }
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

// WithMeter sets the OpenTelemetry meter for the application.
func WithMeter(meter metric.Meter) Option {
	return func(c *config) { c.meter = meter }
}

// statusZoneMapping assigns each status-bar key to a semantic zone.
// The "lifecycle" zone carries the active turn's counters (phase, title,
// and the four token counters sent / received / total / thinking);
// "context" carries thread-level metadata; unmapped keys fall into
// the "default" zone (lowest priority, only rendered if the higher-
// priority zones fit within the 3-line status budget). The thinking
// token is grouped with sent / received / total so the framework's
// compactTokenSegments can fold it into the same ↑ / ↓ / Σ / Ψ
// cluster instead of leaving it as an orphan "tokens" segment in
// the default zone.
//
// Keys listed here must match the keys emitted by the upstream
// handler: x/usage/handler.go emits "sent", "received", "total",
// and "thinking"; the workshop app emits the others via slash
// commands and Stream.SetMetadata in defaultMeta.
var statusZoneMapping = map[string]string{
	"phase":                   "lifecycle",
	"title":                   "lifecycle",
	"thread_id":               "context",
	"cwd":                     "context",
	"git_branch":              "context",
	"workshop.role":           "context",
	"workshop.thinking_level": "context",
	"tui.pid":                 "context",
	"model":                   "context",
	"sent":                    "lifecycle",
	"received":                "lifecycle",
	"total":                   "lifecycle",
	"thinking":                "lifecycle",
}

// RunTUI initializes and starts the TUI application.
func RunTUI(ctx context.Context, opts ...Option) error {
	cfg := &config{conduit: "TUI"}
	for _, opt := range opts {
		opt(cfg)
	}

	// Create a compaction notifier to forward compacted turns to the TUI.
	notifier := &compactionNotifier{}
	cfg.compactionNotifier = notifier

	mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}

	// Create the TUI conduit.
	tuiConduit, err := tui.New(mgr,
		tui.WithThreadID(cfg.threadID),
		tui.WithName("ws"),
		tui.WithTracer(cfg.tracer),
		tui.WithStatusZones(statusZoneMapping),
		tui.WithStatusLabels(map[string]string{
			"workshop.role":           "role",
			"workshop.thinking_level": "thinking",
		}),
	)
	if err != nil {
		return fmt.Errorf("create TUI conduit: %w", err)
	}

	// Wire the notifier to reload the TUI history when compaction occurs.
	if tuiImpl, ok := tuiConduit.(*tui.TUI); ok {
		notifier.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
			_ = tuiImpl.ReloadHistory(turns, boundary) // Best-effort: ignore reload errors to avoid disrupting compaction.
		})
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
// without triggering an LLM turn. With no argument (or an explicit
// "help" subcommand) it returns a feedback message listing the
// current role and the available role definitions. With a name it
// validates the role exists and updates the active resolver's path.
//
// The role change is communicated to the LLM via the system prompt
// transform on the next turn: the transform reads the active role
// file through the resolver, so swapping the resolver's path is
// sufficient to switch what the LLM sees. No persistent RoleSystem
// turn is appended to the conversation history — the system prompt
// itself is the single source of truth for the active role.
type roleCommand struct {
	mu       sync.Mutex
	rdir     string
	stream   *junk.Stream
	resolver *source.FileResolver
}

// SetStream is called from the stepFactory when a new stream is bound.
// It creates a fresh resolver for the stream and seeds it from the
// stream's current role metadata, if any. Existing streams preserve
// their previously-set role; new streams start with no role until
// one is selected via /role.
func (c *roleCommand) SetStream(stream *junk.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stream = stream
	c.resolver = source.NewFileResolver("")
	if role, ok := stream.GetMetadata("workshop.role"); ok && role != "" {
		c.resolver.SetPath(filepath.Join(c.rdir, role+".md"))
	}
}

// Resolver returns the resolver for the current stream. Returns nil
// when no stream is attached. Intended to be passed to
// makeSystemPromptTransform so the system prompt can read the
// active role directly from the file, without going through metadata.
func (c *roleCommand) Resolver() *source.FileResolver {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolver
}

// currentRole returns the active role from stream metadata, or the
// empty string when no role is set (or no stream is attached). The
// empty string is rendered as "(none)" by callers.
func (c *roleCommand) currentRole() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return ""
	}
	v, _ := c.stream.GetMetadata("workshop.role")
	return v
}

// Handler dispatches the /role slash command. With no argument (or
// "help") it lists the available roles. With a name it validates the
// role exists, updates the active resolver's path, and writes the
// role name to stream metadata. An unknown role returns an error so
// the user sees the failure rather than having their active role
// silently changed.
//
// The role change is reflected in the system prompt on the next
// turn (via the resolver); no persistent RoleSystem turn is appended.
func (c *roleCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	args := slash.Fields(cmd.Input)
	// "help" is reserved as a subcommand so that /role help always
	// shows the role list, mirroring /role with no argument. This
	// matches the convention used by other slash commands and means
	// a user-defined role cannot collide with the help affordance.
	if len(args) == 0 || args[0] == "help" {
		return slash.Result{
			Notice: loop.Notice{
				Content:  c.formatRoleList(),
				Severity: loop.SeverityInfo,
			},
		}, nil
	}

	name := args[0]
	if _, err := role.LoadRole(c.rdir, name, nil); err != nil {
		return slash.Result{}, fmt.Errorf("role %q not found: %w", name, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil || c.resolver == nil {
		return slash.Result{}, fmt.Errorf("no active stream")
	}
	// Update the resolver's path. The system prompt transform picks
	// up the new role on the next Transform call. The role metadata
	// is also written so that /role no-arg, thread export, and the
	// TUI status zone continue to display the active role name. No
	// persistent RoleSystem turn is appended; the system prompt
	// itself is the single source of truth.
	c.resolver.SetPath(filepath.Join(c.rdir, name+".md"))
	c.stream.SetMetadata("workshop.role", name)

	return slash.Result{
		Notice: loop.Notice{
			Content:  fmt.Sprintf("Role: %s", name),
			Severity: loop.SeverityInfo,
		},
	}, nil
}

// formatRoleList builds a multi-line help message describing the
// active role and every role definition on disk. The list is sorted
// alphabetically for stable output. Used by the no-arg and "help"
// forms of /role.
func (c *roleCommand) formatRoleList() string {
	current := c.currentRole()
	if current == "" {
		current = "(none)"
	}

	roles, err := role.ListRoleDefinitions(c.rdir, nil)
	if err != nil {
		return fmt.Sprintf("Role: %s\nError reading roles from %s: %v\nUsage: /role <name>",
			current, c.rdir, err)
	}

	if len(roles) == 0 {
		return fmt.Sprintf("Role: %s\nNo roles available in %s\nUsage: /role <name>",
			current, c.rdir)
	}

	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })

	lines := []string{fmt.Sprintf("Role: %s", current), "Available:"}
	for _, r := range roles {
		if r.Description != "" {
			lines = append(lines, fmt.Sprintf("  %s (%s)", r.Name, r.Description))
		} else {
			lines = append(lines, fmt.Sprintf("  %s", r.Name))
		}
	}
	lines = append(lines, "Usage: /role <name>")
	return strings.Join(lines, "\n")
}

// thinkingCommand handles the /thinking slash command for changing
// the active thread's thinking level without triggering an LLM turn.
// The level is stored in stream metadata under "workshop.thinking_level"
// so it persists across turns and across thread resume. SetMetadata
// emits a loop.PropertiesEvent so the TUI status bar updates in real
// time; buildInvokeOptions reads the same key at request time.
type thinkingCommand struct {
	mu     sync.Mutex
	stream *junk.Stream
}

// SetStream updates the shared stream reference. Called by the
// stepFactory on every stream open.
func (c *thinkingCommand) SetStream(s *junk.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stream = s
}

// currentThinkingLevel reads the active stream's thinking level from
// metadata, defaulting to ThinkingLevelOff when unset. The empty
// string is treated as off, matching resolveThinkingLevel's contract.
func (c *thinkingCommand) currentThinkingLevel() models.ThinkingLevel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return models.ThinkingLevelOff
	}
	v, ok := c.stream.GetMetadata("workshop.thinking_level")
	if !ok || v == "" {
		return models.ThinkingLevelOff
	}
	level, err := models.ParseThinkingLevel(v)
	if err != nil {
		return models.ThinkingLevelOff
	}
	return level
}

// Handler validates the level name and updates the stream metadata.
// With no argument, returns the current level and the list of
// available levels as a Result.Feedback message. An unknown level
// returns a feedback message and leaves state unchanged. Successful
// sets also return a feedback message confirming the change.
func (c *thinkingCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	args := slash.Fields(cmd.Input)
	current := c.currentThinkingLevel()

	if len(args) == 0 {
		// No-arg form: report current + available levels.
		available := []string{
			string(models.ThinkingLevelOff),
			string(models.ThinkingLevelMinimal),
			string(models.ThinkingLevelLow),
			string(models.ThinkingLevelMedium),
			string(models.ThinkingLevelHigh),
			string(models.ThinkingLevelMax),
		}
		return slash.Result{
			Notice: loop.Notice{
				Content: fmt.Sprintf("Thinking: %s\nLevels: %s\nUsage: /thinking <level>",
					current, strings.Join(available, ", ")),
				Severity: loop.SeverityInfo,
			},
		}, nil
	}

	wanted := args[0]
	level, err := models.ParseThinkingLevel(wanted)
	if err != nil {
		// Unknown level: report the error but do not mutate.
		return slash.Result{
			Notice: loop.Notice{
				Content:  fmt.Sprintf("Unknown level: %s. Available: off, minimal, low, medium, high, max", wanted),
				Severity: loop.SeverityError,
			},
		}, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return slash.Result{}, fmt.Errorf("no active stream")
	}
	c.stream.SetMetadata("workshop.thinking_level", string(level))
	return slash.Result{
		Notice: loop.Notice{
			Content:  fmt.Sprintf("Thinking: %s", level),
			Severity: loop.SeverityInfo,
		},
	}, nil
}

// compactCommand handles the /compact slash command for forcing
// conversation compaction without triggering an LLM turn.
//
// In the ore compaction redesign, /compact is the only entry point:
// compaction is non-destructive and explicitly invoked. The handler
// calls compaction.Summarize to obtain a single RoleSystem turn
// carrying both the LLM-facing summary and the artifact.Compaction
// metadata, then appends it to the stream via AppendTurn. On
// ErrTruncatedSummary the buffer is left untouched and the user is
// told why.
type compactCommand struct {
	mu       sync.Mutex
	stream   *junk.Stream
	agent    *agent.Agent
	notifier *compactionNotifier
}

// Handler forces an immediate compaction of the active thread's state.
// /compact is always available when a provider is configured; buildManager
// always wires the handler with one, so the kill-switch path that used to
// return "compaction is not enabled" for MaxTokens <= 0 is gone. The event
// is consumed (nil, nil) so no LLM inference is triggered.
func (c *compactCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return slash.Result{}, fmt.Errorf("no active stream")
	}
	turns := c.stream.Turns()
	if len(turns) == 0 {
		return slash.Result{}, fmt.Errorf("no turns to compact")
	}
	turn, info, err := compaction.Summarize(ctx, c.agent, turns)
	if err != nil {
		// Truncation: the model hit its output cap mid-summary. Leave
		// the buffer unchanged and surface the failure to the user.
		if errors.Is(err, compaction.ErrTruncatedSummary) {
			return slash.Result{
				Notice: loop.Notice{
					Content:  "Compaction truncated: model hit its output cap mid-summary; history unchanged.",
					Severity: loop.SeverityWarn,
				},
			}, nil
		}
		return slash.Result{}, err
	}
	if err := c.stream.AppendTurn(ctx, turn.Role, turn.Artifacts...); err != nil {
		return slash.Result{}, fmt.Errorf("append compaction turn: %w", err)
	}
	// Record the boundary on state.Meta so the next Transform call
	// projects the buffer from the compaction turn onward. The boundary
	// index is the position of the just-appended summary turn. MarkBoundary
	// takes a pre-encoded JSON string for the boundary info to keep the
	// session package free of any x/compaction dependency.
	boundaryIdx := len(c.stream.Turns()) - 1
	encoded, err := compaction.EncodeBoundaryInfo(info)
	if err != nil {
		return slash.Result{}, fmt.Errorf("encode boundary info: %w", err)
	}
	if err := c.stream.MarkBoundary(boundaryIdx, encoded); err != nil {
		return slash.Result{}, fmt.Errorf("mark boundary: %w", err)
	}
	if c.notifier != nil {
		c.notifier.Notify(c.stream.Turns(), info)
	}
	if err := c.stream.Save(); err != nil {
		return slash.Result{}, fmt.Errorf("save thread: %w", err)
	}
	return slash.Result{}, nil
}

// SetStream updates the shared stream reference.
func (c *compactCommand) SetStream(s *junk.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stream = s
}

// analyticsCommand handles the /analytics slash command for surfacing a
// per-(Kind, Source) byte and count breakdown of the current thread's
// artifacts. The handler is read-only: it never invokes the LLM, never
// mutates state, and never appears as a tool the model can call.
// /analytics is slash-only by design so the model cannot spend context
// budget calling it.
type analyticsCommand struct {
	mu     sync.Mutex
	stream *junk.Stream
}

// Handler analyzes the current thread's turns and renders the result
// as a Markdown table. When the active stream is unset (e.g. the
// command is invoked from a unit test without going through the
// session pipeline), the friendly empty-state message is returned
// rather than panicking. The event is consumed (no Result.Replace) so
// no LLM inference is triggered.
func (c *analyticsCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return slash.Result{
			Notice: loop.Notice{
				Content:  "No artifacts in this thread yet.",
				Severity: loop.SeverityInfo,
			},
		}, nil
	}
	stats := analytics.AnalyzeTurns(c.stream.Turns())
	return slash.Result{
		Notice: loop.Notice{
			Content:  analytics.Render(stats),
			Severity: loop.SeverityInfo,
		},
	}, nil
}

// SetStream updates the shared stream reference. Called by the
// stepFactory on every stream open.
func (c *analyticsCommand) SetStream(s *junk.Stream) {
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
func buildManager(cfg *config) (*junk.Manager, error) {
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
	store, err := junk.NewJSONStore(storeDir)
	if err != nil {
		return nil, fmt.Errorf("create JSON store: %w", err)
	}

	// Build the providers: validate every defined named provider,
	// compile each one, and resolve the default (inference) name
	// plus the compaction name (which falls back to the default
	// when unset).
	compiled, err := compileProviders(cfg, tracer)
	if err != nil {
		return nil, err
	}
	prov := compiled[cfg.defaultProviderName]
	compactionName := cfg.compaction.Provider
	if compactionName == "" {
		compactionName = cfg.defaultProviderName
	}
	if _, ok := compiled[compactionName]; !ok {
		return nil, fmt.Errorf("compaction.provider %q is not defined in providers: section (defined: %s)", compactionName, definedProviderNamesAsCompiledKeys(compiled))
	}
	compactionProv := compiled[compactionName]

	// Build the compact command handler. Compaction is explicit-only
	// (the /compact slash command); there is no automatic trigger.
	// The handler is always wired with an agent, so /compact is
	// always reachable when this manager runs. When MaxTokens is
	// <= 0, MaxOutputTokens is 0, which the ore/compaction package
	// treats as "use framework default" (8192).
	ccSpec := models.Spec{
		Name:            cfg.providers[compactionName].Model,
		MaxOutputTokens: int64(cfg.compaction.MaxTokens),
	}

	// The compaction agent carries the compactor's provider + spec +
	// a SingleShot cognitive pattern. compaction.Summarize wires it
	// up to drive one inference turn; the agent's lifecycle is owned
	// by this manager and shared across /compact invocations.
	ccAgent := agent.New(
		"compactor",
		agent.WithProvider(compactionProv),
		agent.WithSpec(ccSpec),
		agent.WithPattern(&cognitive.SingleShot{}),
	)

	// Build the default model spec carried by every loop invocation.
	// Model identity, sampling params, and output budget live on the
	// spec in ore v0.12; per-thread overrides flow through stream
	// metadata (Stream.Spec). The spec is captured by the step
	// factory closure below.
	defaultSpec := buildDefaultSpec(cfg.defaultProviderConfig())

	// Create role command handler.
	rc := &roleCommand{rdir: role.Dir()}

	// Create compact command handler.
	cc := &compactCommand{agent: ccAgent, notifier: cfg.compactionNotifier}

	// Create thinking-level command handler.
	tc := &thinkingCommand{}

	// Create analytics command handler.
	ac := &analyticsCommand{}

	// Create slash command registry.
	slashReg := slash.NewRegistry()
	slashReg.Bind("role", "Show the current role and available roles, or switch to one by name", rc.Handler)
	slashReg.Bind("compact", "Compact conversation history", cc.Handler)
	slashReg.Bind("thinking", "Set the thinking level for this thread", tc.Handler)
	slashReg.Bind("analytics", "Show per-(kind, source) byte and count breakdown for this thread", ac.Handler)
	slashReg.Bind("name", "Set the conversation title", settitle.Slash())

	// Step factory: inject system prompt and guardrails as transforms.
	stepFactory := func(stream *junk.Stream) ([]loop.Option, error) {
		rc.SetStream(stream)
		cc.SetStream(stream)
		tc.SetStream(stream)
		ac.SetStream(stream)

		// Set up progressive skill discovery. Built-in skills (the
		// framework-shipped registry, e.g. `writing-skills`) are
		// authoritative: passing skills.BuiltInSkills first lets the
		// framework's defaults win on name collision, matching the
		// composition pattern in x/tool/skills/doc.go. Repo-local
		// (.agents/skills) and user-global (~/.agents/skills) discoverers
		// follow so any unique skills they expose are added to the catalog.
		var discoverers []skills.Discoverer
		discoverers = append(discoverers, skills.BuiltInSkills)
		discoverers = append(discoverers, skills.NewFSDiscoverer(".agents/skills"))
		if homeDir, err := os.UserHomeDir(); err == nil {
			discoverers = append(discoverers, skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills")))
		}
		skillsToolkit := skills.NewToolkit(discoverers...)

		// Build the composable system prompt transform.
		sp, err := makeSystemPromptTransform(cfg, stream, skillsToolkit, rc.Resolver())
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
		mustRegisterRaw(registry, "workspace_destroy", "Remove the git worktree created in this junk.", destroyWorkspaceSchema, makeWorkspaceDestroyHandler(stream))
		mustRegisterRaw(registry, "git_commit", "Commit staged changes with automatic co-author attribution.", gitCommitSchema, makeGitCommitHandler(stream, cfg.defaultProviderConfig()))

		// Title management.
		mustRegisterRaw(registry, "set_title", "Set the conversation title visible to all conduits.", setTitleSchema, settitle.Tool())

		invokeOpts := buildInvokeOptions(cfg, registry.Tools())

		tel := telemetry.New(cfg.meter)

		return []loop.Option{
			// compaction.NewTransform projects the LLM-facing view
			// through the latest artifact.Compaction in the buffer. It
			// must sit between the system prompt (which prepends the
			// persona) and guardrails (which append safety rules on top),
			// so the summary stands in for everything older than itself.
			loop.WithTransforms(sp, compaction.NewTransform(), gr),
			loop.WithHandlers(xtool.NewHandler(registry, xtool.WithTracer(tracer)), usage.New()),
			loop.WithInvokeOptions(invokeOpts...),
			loop.WithDefaultSpec(defaultSpec),
			loop.WithTracer(tracer),
			loop.WithOnEmit(tel.OnEmit()),
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

	defaultMeta := func(stream *junk.Stream) map[string]string {
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
		}
		if role != "" {
			defaults["workshop.role"] = role
		}
		defaults["tui.pid"] = strconv.Itoa(os.Getpid())
		return defaults
	}

	// Wrap the ReAct processor. Compaction in ore v0.12 is explicit-only
	// (the /compact slash command); there is no automatic pre-turn
	// trigger, so the processor is the framework ReAct processor with
	// no extra wrapping. The processor receives the per-turn spec from
	// the session manager (built from Stream.Spec, which itself reads
	// the per-thread metadata); we forward it to the ReAct pattern as-is.
	processor := func(ctx context.Context, step *loop.Step, st state.State, prov provider.Provider, spec models.Spec) (state.State, error) {
		return cognitive.NewTurnProcessor(cognitive.ReActFactory, tracer)(ctx, step, st, prov, spec)
	}

	// Create session manager.
	return junk.NewManager(store, prov, stepFactory, processor, junk.WithDefaultMetadata(defaultMeta), junk.WithInterceptor(slashReg)), nil
}

// makeSystemPromptTransform builds the composable system prompt transform for
// a given configuration and metadata reader. It concatenates four content sources:
//
//  1. The active role prompt read from the resolver's current file path
//     (or defaultPrompt if no role is set). The resolver is mutated in
//     place by the role command when the active role changes; the
//     transform reads whatever path is current at Transform-time.
//  2. A contextual sentence describing the current working directory.
//  3. The skills catalog fragment showing available skills to the LLM.
//  4. Repository-level instructions discovered by walking parent directories
//     from cfg.workingDir toward the root, collecting AGENTS.md and
//     CLAUDE.md files nearest-first.
//
// The resulting transform is passed to loop.Step via loop.WithTransforms.
func makeSystemPromptTransform(cfg *config, _ metadataReader, skillsToolkit *skills.Toolkit, roleResolver *source.FileResolver) (loop.Transform, error) {
	return systemprompt.New(
		systemprompt.WithContentFunc(func() string {
			path := roleResolver.Path()
			if path == "" {
				return defaultPrompt
			}
			body, err := role.LoadBody(path, nil)
			if err != nil {
				return defaultPrompt
			}
			return body
		}),
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
			pc := cfg.defaultProviderConfig()
			if pc.Model == "" {
				return ""
			}
			return "You are running on model " + pc.Model + "."
		}),
		systemprompt.WithContentFunc(func() string {
			pc := cfg.defaultProviderConfig()
			if pc.Kind == "" {
				return ""
			}
			return "Provider backend: " + pc.Kind
		}),
	)
}

// defaultAnthropicMaxTokens is the workshop-side default for the Anthropic
// provider's required `max_tokens` field. The Anthropic SDK rejects a value
// of 0, so callers that leave ProviderConfig.MaxTokens unset get this value
// applied to models.Spec.MaxOutputTokens at spec-build time. 32k fits
// comfortably inside Sonnet 4.5's 64k output ceiling while leaving room for
// typical extended-thinking budgets.
const defaultAnthropicMaxTokens int64 = 32000

// buildDefaultSpec assembles the default models.Spec carried by every
// loop invocation. Model identity and inference configuration live on the
// spec in ore v0.12 (Spec.Name, Spec.MaxOutputTokens, Spec.Temperature,
// Spec.ThinkingLevel), so they are not baked into the provider at
// construction time. Per-thread overrides flow through stream metadata
// via the Stream.Spec() helper, which takes precedence over the
// per-loop default.
//
// Anthropic-specific: when MaxTokens is left at 0, defaultAnthropicMaxTokens
// is applied so the Anthropic SDK does not reject the request with
// max_tokens=0. The OpenAI path does not require a default (its SDK accepts
// an unset max_tokens).
//
// Temperature is forwarded as *float64 to mirror the spec field's "nil means
// use the model default" convention; a zero value from the user config is
// treated as "no opinion".
func buildDefaultSpec(pc ProviderConfig) models.Spec {
	spec := models.Spec{
		Name: pc.Model,
	}
	if pc.Temperature != 0 {
		t := pc.Temperature
		spec.Temperature = &t
	}
	if level := resolveThinkingLevel(pc.ThinkingLevel); level != "" {
		spec.ThinkingLevel = level
	}
	maxTokens := pc.MaxTokens
	if pc.Kind == "anthropic" && maxTokens == 0 {
		maxTokens = defaultAnthropicMaxTokens
	}
	if maxTokens > 0 {
		spec.MaxOutputTokens = maxTokens
	}
	return spec
}

// buildInvokeOptions assembles the per-invocation options for the configured
// provider. It branches on the default provider's Kind so the right
// per-provider options are applied for each backend. Per-call model
// identity and inference configuration live on models.Spec (see
// buildDefaultSpec and Stream.Spec); buildInvokeOptions only carries
// provider-specific options that have no spec equivalent (currently just
// the tool list).
func buildInvokeOptions(cfg *config, tools []tool.Tool) []provider.InvokeOption {
	pc := cfg.defaultProviderConfig()
	var opts []provider.InvokeOption
	switch pc.Kind {
	case "anthropic":
		opts = append(opts, anthropic.WithTools(tools))
	default:
		// OpenAI-compatible path (Kind == "" or "openai").
		opts = append(opts, openai.WithTools(tools))
	}
	return opts
}

// resolveThinkingLevel parses the user-supplied level string and
// returns a normalized ThinkingLevel. The empty string and any
// unrecognized value are treated as ThinkingLevelOff. This is the
// single source of truth for "user did not set a level" semantics
// across the workshop.
func resolveThinkingLevel(s string) models.ThinkingLevel {
	if s == "" {
		return models.ThinkingLevelOff
	}
	level, err := models.ParseThinkingLevel(s)
	if err != nil {
		return models.ThinkingLevelOff
	}
	return level
}

// wrapWithRetry wraps a provider.Provider with the workshop's
// hardcoded retry policy: 5 attempts, 500ms base delay, 10s cap,
// default classifier (5xx + 429 + Retry-After). When tracer is
// non-nil, retry.invoke spans are emitted as parents of the
// inner provider's spans.
func wrapWithRetry(p provider.Provider, tracer trace.Tracer) provider.Provider {
	opts := []retry.Option{
		retry.WithMaxAttempts(5),
		retry.WithBaseDelay(500 * time.Millisecond),
		retry.WithMaxDelay(10 * time.Second),
	}
	if tracer != nil {
		opts = append(opts, retry.WithTracer(tracer))
	}
	return retry.New(p, opts...)
}

// newProvider constructs a provider.Provider from generic ProviderConfig.
//
// newProvider takes a pointer to ProviderConfig because the anthropic
// branch mutates pc.MaxTokens to apply the default; a value-pass would
// discard that mutation, causing buildInvokeOptions to see a zero value
// and skip the WithMaxTokens option (which would then default to
// max_tokens=1 on the wire).
func newProvider(name string, pc *ProviderConfig, tracer trace.Tracer) (provider.Provider, error) {
	switch pc.Kind {
	case "", "openai":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("missing required provider config: api_key")
		}
		if pc.Model == "" {
			return nil, fmt.Errorf("missing required provider config: model")
		}
		// Model identity is no longer carried by the provider in ore
		// v0.12; it is supplied per-invocation via models.Spec.Name
		// (configured on the loop as the default spec).
		var opts []openai.Option
		opts = append(opts, openai.WithAPIKey(pc.APIKey))
		if pc.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.BaseURL))
		}
		if tracer != nil {
			opts = append(opts, openai.WithTracer(tracer))
		}
		inner, err := openai.New(opts...)
		if err != nil {
			return nil, err
		}
		return wrapWithRetry(inner, tracer), nil
	case "anthropic":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("missing required provider config: api_key")
		}
		if pc.Model == "" {
			return nil, fmt.Errorf("missing required provider config: model")
		}
		// Model identity is supplied per-invocation via models.Spec.Name
		// (see buildDefaultSpec). MaxTokens is now carried by
		// Spec.MaxOutputTokens and the workshop default is applied at
		// spec-build time.
		var opts []anthropic.Option
		opts = append(opts, anthropic.WithAPIKey(pc.APIKey))
		if pc.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(pc.BaseURL))
		}
		if tracer != nil {
			opts = append(opts, anthropic.WithTracer(tracer))
		}
		inner, err := anthropic.New(opts...)
		if err != nil {
			return nil, err
		}
		return wrapWithRetry(inner, tracer), nil
	default:
		return nil, fmt.Errorf("unsupported provider kind: %q", pc.Kind)
	}
}

// compileProviders validates every defined named provider, compiles
// each one through newProvider, and returns a map from name to
// compiled provider.Provider. It is the single source of truth for
// the per-named-provider validation contract:
//
//   - At least one provider must be defined.
//   - The defaultProviderName must reference a defined name.
//   - Every defined name must have a non-empty api-key and model.
//   - Every defined name must have a known kind (or "" for openai).
//
// Errors include the offending name so a misconfigured config points
// the operator at the right entry. newProvider no longer mutates the
// per-name config (in ore v0.12 model identity lives on the per-turn
// spec, not on the provider); the write-back below is a defensive
// copy so readers see a value that mirrors what was passed in.
func compileProviders(cfg *config, tracer trace.Tracer) (map[string]provider.Provider, error) {
	if len(cfg.providers) == 0 {
		return nil, fmt.Errorf("no providers defined; configure the providers: section in config.yaml")
	}
	if cfg.defaultProviderName == "" {
		return nil, fmt.Errorf("provider: <name> is required; set the name of the default inference provider")
	}
	if _, ok := cfg.providers[cfg.defaultProviderName]; !ok {
		return nil, fmt.Errorf("default provider %q is not defined in providers:", cfg.defaultProviderName)
	}

	out := make(map[string]provider.Provider, len(cfg.providers))
	for name := range cfg.providers {
		pc := cfg.providers[name]
		prov, err := newProvider(name, &pc, tracer)
		if err != nil {
			return nil, fmt.Errorf("create provider %q: %w", name, err)
		}
		cfg.providers[name] = pc
		out[name] = prov
	}
	return out, nil
}

// definedProviderNamesAsCompiledKeys returns the keys of the compiled
// provider map in sorted order, used for error messages that need to
// list "the defined providers are X, Y, Z". Equivalent to
// definedProviderNames in cmd/workshop, but operates on the compiled
// (post-validate) map rather than the raw config.
func definedProviderNamesAsCompiledKeys(m map[string]provider.Provider) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
