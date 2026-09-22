// Package app provides the core application logic for the workshop coding
// assistant. It wires together the ore framework's TUI conduit, HTTP web UI
// conduit, system prompt transforms, guardrails, and tool registry to create
// an interactive coding agent.
//
// The system prompt combines the default prompt, current working directory,
// available skills, runtime details, and repository instructions discovered by
// walking from the working directory toward the root.
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
	"github.com/andrewhowdencom/ore/engine"
	"github.com/andrewhowdencom/ore/ledger"
	state "github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/tool"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/andrewhowdencom/ore/x/analytics"
	"github.com/andrewhowdencom/ore/x/compaction"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
	stdioc "github.com/andrewhowdencom/ore/x/conduit/stdio"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	"github.com/andrewhowdencom/ore/x/guardrails"
	"github.com/andrewhowdencom/ore/x/provider/anthropic"
	"github.com/andrewhowdencom/ore/x/provider/codex"
	"github.com/andrewhowdencom/ore/x/provider/openai"
	"github.com/andrewhowdencom/ore/x/provider/retry"
	slash "github.com/andrewhowdencom/ore/x/slash"
	"github.com/andrewhowdencom/ore/x/systemprompt"
	"github.com/andrewhowdencom/ore/x/systemprompt/source"
	"github.com/andrewhowdencom/ore/x/telemetry"
	xtool "github.com/andrewhowdencom/ore/x/tool"
	settitle "github.com/andrewhowdencom/ore/x/tool/set_title"
	"github.com/andrewhowdencom/ore/x/tool/skills"
	"github.com/andrewhowdencom/ore/x/usage"

	"github.com/adrg/xdg"

	"github.com/andrewhowdencom/workshop/internal/subagent"
)

// ProviderConfig holds the user-supplied configuration for a concrete provider.
type ProviderConfig struct {
	Kind        string // "openai", "anthropic", or "codex"
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
	// CacheControl enables Anthropic prompt caching on the request.
	// Empty means "no cache control" (the framework default; the
	// request body is byte-equivalent to the pre-change shape).
	// The value is parsed as a Go duration string ("5m", "1h", or
	// any other time.ParseDuration-accepting form). The framework's
	// canonical 5m / 1h constants are the values Anthropic's API
	// accepts; other durations are forwarded verbatim and may be
	// rejected by the upstream at request time. Invalid
	// duration strings are silently dropped at spec-build time
	// because buildDefaultSpec is not the right place to fail
	// loudly. The field flows through models.Spec.CacheControl.TTL
	// when set; the Anthropic wire stamps Anthropic-style
	// cache_control blocks at the system message, the last tool
	// definition, and the last user/assistant text content
	// part. OpenAI-compatible providers silently ignore it.
	CacheControl string
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

	setup, err := setupSession(cfg)
	if err != nil {
		return err
	}

	// Construct the active session. TUI always creates a fresh
	// session (TUI never attaches to an existing threadID — the
	// `--thread` flag selects the threaded entrypoint used by
	// stdio, not TUI).
	sess := setup.newSession()
	if err := setup.registry.Register(sess); err != nil {
		return fmt.Errorf("register session: %w", err)
	}
	setup.seedMetadata(sess)

	// The TUI's cancel pathway flows through event-context
	// propagation. We derive runCtx from the parent signal-derived
	// ctx and pass it to the TUI as its event context. The TUI
	// wraps it internally and cancels the wrapper on Esc / Ctrl+C;
	// the cancellation propagates through the event's Context()
	// into the engine's per-event ctx, unwinding the running agent.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	tuiConduit, err := tui.New(sess,
		tui.WithName("ws"),
		tui.WithEventContext(runCtx),
		tui.WithTracer(cfg.tracer),
		tui.WithStatusZones(statusZoneMapping),
		tui.WithStatusLabels(map[string]string{
			"workshop.thinking_level": "thinking",
		}),
	)
	if err != nil {
		return fmt.Errorf("create TUI conduit: %w", err)
	}

	// Wire the notifier to reload the TUI history when compaction occurs.
	tuiImpl, ok := tuiConduit.(*tui.TUI)
	if !ok {
		return fmt.Errorf("TUI conduit does not implement *tui.TUI")
	}
	notifier.SetReloader(func(turns []state.Turn, boundary compaction.BoundaryInfo) {
		_ = tuiImpl.ReloadHistory(turns, boundary) // Best-effort: ignore reload errors to avoid disrupting compaction.
	})

	return runTUIEngine(runCtx, sess, tuiImpl, setup.factory, setup.engine, setup.repo)
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

	setup, err := setupSession(cfg)
	if err != nil {
		return err
	}

	// Create the HTTP conduit with web UI enabled. The HTTP
	// conduit consumes a Backend interface; sessionBackend
	// adapts the registry/repo/engine onto that surface.
	httpConduit, err := httpc.New(
		newSessionBackend(setup.registry, setup.repo, setup.engine),
		httpc.WithUI(),
		httpc.WithName("workshop"),
		httpc.WithAddr(cfg.httpAddr),
		httpc.WithTracer(cfg.tracer),
	)
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

	setup, err := setupSession(cfg)
	if err != nil {
		return err
	}

	// Construct or attach a session.
	var sess *session.Session
	if cfg.threadID != "" {
		sess, err = setup.attachSession(ctx, cfg.threadID)
		if err != nil {
			return fmt.Errorf("attach session: %w", err)
		}
	} else {
		sess = setup.newSession()
	}
	if err := setup.registry.Register(sess); err != nil {
		return fmt.Errorf("register session: %w", err)
	}
	setup.seedMetadata(sess)

	// Create the stdio conduit. Stdio is session-shaped in ore#550
	// (verified at ore/x/conduit/stdio/stdio.go:80): it accepts a
	// *session.Session directly, no manager adapter.
	stdioConduit, err := stdioc.New(sess,
		stdioc.WithTracer(cfg.tracer),
	)
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

// thinkingCommand handles the /thinking slash command for changing
// the active thread's thinking level without triggering an LLM turn.
// The level is stored in stream metadata under "workshop.thinking_level"
// so it persists across turns and across thread resume. SetMetadata
// emits a loop.PropertiesEvent so the TUI status bar updates in real
// time; buildInvokeOptions reads the same key at request time.
type thinkingCommand struct {
	mu      sync.Mutex
	session *session.Session
}

// SetSession updates the shared session reference. Called by the
// tui engine factory on every Build call (one per dequeued event).
//
// The slash handler reads and writes through the *session.Session directly so
// it uses the same state as every conduit.
func (c *thinkingCommand) SetSession(sess *session.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = sess
}

// currentThinkingLevel reads the active session's thinking level
// from metadata, defaulting to ThinkingLevelOff when unset. The
// empty string is treated as off, matching resolveThinkingLevel's
// contract.
func (c *thinkingCommand) currentThinkingLevel() models.ThinkingLevel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return models.ThinkingLevelOff
	}
	v, ok := c.session.GetMetadata("workshop.thinking_level")
	if !ok || v == "" {
		return models.ThinkingLevelOff
	}
	level, err := models.ParseThinkingLevel(v)
	if err != nil {
		return models.ThinkingLevelOff
	}
	return level
}

// writeLevel persists the active thinking level in thread metadata and emits a
// PropertiesEvent through session metadata for live status updates.
//
// The caller MUST hold c.mu. writeLevel does not lock because the
// only callers (Handler) already hold the lock. Re-locking here
// would deadlock.
func (c *thinkingCommand) writeLevel(level string) {
	if c.session == nil {
		return
	}
	c.session.Thread().Meta().Set("workshop.thinking_level", level)
	c.session.SetMetadata("workshop.thinking_level", level)
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
	if c.session == nil {
		c.mu.Unlock()
		return slash.Result{}, fmt.Errorf("no active session")
	}
	c.writeLevel(string(level))
	c.mu.Unlock()
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
// metadata, then appends it to the session via Submit. On
// ErrTruncatedSummary the buffer is left untouched and the user is
// told why.
type compactCommand struct {
	mu       sync.Mutex
	session  *session.Session
	agent    *agent.Agent
	notifier *compactionNotifier
}

// Handler forces an immediate compaction of the active thread's state.
// /compact is always available when a provider is configured; buildManager
// always wires the handler with one, so the kill-switch path that used to
// return "compaction is not enabled" for MaxTokens <= 0 is gone. The event
// is consumed (nil, nil) so no LLM inference is triggered.
//
// session-based design: the summary turn is appended via
// session.Submit (which auto-appends to the bound thread), and the
// boundary info is written to session metadata under
// compaction.MetaKeyBoundaryInfo. The framework's
// readBoundaryFromSession reads from the same key. No explicit Save
// call here — persistence is the responsibility of the runTUIEngine
// lifecycle pump, which saves on every LifecycleEvent "done" emitted
// by the engine. The pre-bump handler called stream.Save() inline;
// that was a pre-migration convenience and is no longer needed
// here.
func (c *compactCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return slash.Result{}, fmt.Errorf("no active session")
	}
	turns := c.session.Turns()
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
	// session.Submit auto-appends to the bound thread via WithState.
	if _, err := c.session.Submit(ctx, turn.Role, turn.Artifacts...); err != nil {
		return slash.Result{}, fmt.Errorf("append compaction turn: %w", err)
	}
	// Set ControlStop on the summary turn (the just-appended one)
	// so the active-path walk stops there. Without this,
	// session.Turns() returns ALL turns (including the originals),
	// and the LLM-facing view bleeds past the compaction boundary.
	// Pre-bump, stream.MarkBoundary did this; in the session-based
	// design we set ControlStop directly on the thread. Re-read
	// turns here because `turns` (declared above) was the
	// pre-submit state.
	postSubmitTurns := c.session.Turns()
	summaryID := postSubmitTurns[len(postSubmitTurns)-1].ID
	c.session.Thread().SetControl(summaryID, state.ControlStop)

	// Record the boundary under the framework's key. The dual-write is
	// load-bearing, matching thinkingCommand.writeLevel:
	//
	//   - thread.Metadata is not journaled in the new model;
	//     disk. Without this, /compact's effect would not survive
	//     a TUI restart.
	//   - session.SetMetadata drives the TUI's readBoundaryFromSession
	//     (see x/conduit/tui/tui.go), which checks
	//     session.GetMetadata.
	encoded, err := compaction.EncodeBoundaryInfo(info)
	if err != nil {
		return slash.Result{}, fmt.Errorf("encode boundary info: %w", err)
	}
	c.session.Thread().Meta().Set(compaction.MetaKeyBoundaryInfo, encoded)
	c.session.SetMetadata(compaction.MetaKeyBoundaryInfo, encoded)
	if c.notifier != nil {
		c.notifier.Notify(c.session.Turns(), info)
	}
	return slash.Result{}, nil
}

// SetSession updates the shared session reference. Called by the
// tui engine factory on every Build call.
func (c *compactCommand) SetSession(sess *session.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = sess
}

// analyticsCommand handles the /analytics slash command for surfacing a
// per-(Kind, Source) byte and count breakdown of the current thread's
// artifacts. The handler is read-only: it never invokes the LLM, never
// mutates state, and never appears as a tool the model can call.
// /analytics is slash-only by design so the model cannot spend context
// budget calling it.
type analyticsCommand struct {
	mu      sync.Mutex
	session *session.Session
}

// Handler analyzes the current thread's turns and renders the result
// as a Markdown table. When the active session is unset (e.g. the
// command is invoked from a unit test without going through the
// session pipeline), the friendly empty-state message is returned
// rather than panicking. The event is consumed (no Result.Replace) so
// no LLM inference is triggered.
func (c *analyticsCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return slash.Result{
			Notice: loop.Notice{
				Content:  "No artifacts in this thread yet.",
				Severity: loop.SeverityInfo,
			},
		}, nil
	}
	stats := analytics.AnalyzeTurns(c.session.Turns())
	return slash.Result{
		Notice: loop.Notice{
			Content:  analytics.Render(stats),
			Severity: loop.SeverityInfo,
		},
	}, nil
}

// SetSession updates the shared session reference. Called by the
// tui engine factory on every Build call.
func (c *analyticsCommand) SetSession(sess *session.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = sess
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

// sessionSetup is the shared wiring that RunTUI, RunHTTP, and
// RunStdio all build on top of. It holds the durable store, the
// active-session registry, the engine that drives inference, and the
// slash registry + handlers that intercept slash events before they
// reach the engine.
//
// Setup is cheap. Each Run* function constructs one of these per
// process invocation; the registry is in-memory; the engine only
// spawns goroutines when Submit is called (so unused setups cost
// essentially nothing).
type sessionSetup struct {
	cfg  *config
	repo ledger.Repository
	// registry holds active *session.Session values for the lifetime
	// of the process. Conduits register the session they drive; the
	// engine resolves it via Get on every Submit.
	registry session.Registry
	// engine drives inference via the factory below. Constructed
	// once per process so every conduit shares the same per-session
	// mailbox.
	engine *engine.Engine
	// factory is shared between conduits; TUI uses the slash handler
	// bindings in Build, stdio and HTTP don't drive slash through
	// this factory (they surface slash via their own conduits or
	// not at all), so the bindings are dormant for them.
	factory *tuiEngineFactory
	// slashReg is invoked by runTUIEngine on every TUI event
	// before Submit. Stdio and HTTP don't currently route events
	// through slashReg.
	slashReg slash.Registry
	// handlers are the slash command implementations. They are
	// session-bound by the factory on every Build (TUI) or not at
	// all (stdio, HTTP).
	handlers slashHandlers
	// defaultSpec is the per-turn model spec captured by the
	// factory's stepFactory closure.
	defaultSpec models.Spec
	// meter is the configured OpenTelemetry meter (or noop fallback)
	// forwarded into the factory's telemetry.OnEmit hook.
	meter metric.Meter
}

// setupSession constructs the shared wiring for one process
// invocation: it opens the ledger-backed durable store, compiles
// the named providers, builds the slash command handlers and
// registry, and instantiates the engine that drives inference.
//
// Callers (RunTUI, RunHTTP, RunStdio) then construct or attach a
// session against the registry and pass it to their conduit.
//
// setupSession deliberately does NOT call NewManager/WithDefaultMetadata
// equivalents — there is no longer a central session orchestrator.
// Each Run* function constructs its own.
func setupSession(cfg *config) (*sessionSetup, error) {
	tracer := cfg.tracer
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer("")
	}
	meter := cfg.meter
	if meter == nil {
		meter = metricnoop.NewMeterProvider().Meter("")
	}

	// Open the ledger-backed durable store. Keep this fallback in
	// sync with cmd/workshop/defaultStoreDir().
	storeDir := cfg.storeDir
	if storeDir == "" {
		storeDir = filepath.Join(xdg.DataHome, "workshop", "threads")
	}
	repo, err := ledger.NewFileRepository(storeDir)
	if err != nil {
		return nil, fmt.Errorf("create ledger repo: %w", err)
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

	// Build the compaction agent. The handler uses it to drive
	// single-shot summary turns.
	ccSpec := models.Spec{
		Name:            cfg.providers[compactionName].Model,
		MaxOutputTokens: int64(cfg.compaction.MaxTokens),
	}
	ccAgent := agent.New(
		"compactor",
		agent.WithProvider(compactionProv),
		agent.WithSpec(ccSpec),
		agent.WithPattern(&cognitive.SingleShot{}),
	)

	defaultSpec := buildDefaultSpec(cfg.defaultProviderConfig())

	// Slash command handlers. The thinking and analytics handlers read
	// from session metadata; the compact handler
	// additionally owns a compaction agent.
	cc := &compactCommand{agent: ccAgent, notifier: cfg.compactionNotifier}
	tc := &thinkingCommand{}
	ac := &analyticsCommand{}

	slashReg := slash.NewRegistry()
	slashReg.Bind("compact", "Compact conversation history", cc.Handler)
	slashReg.Bind("thinking", "Set the thinking level for this thread", tc.Handler)
	slashReg.Bind("analytics", "Show per-(kind, source) byte and count breakdown for this thread", ac.Handler)
	slashReg.Bind("name", "Set the conversation title", settitle.Slash())

	// stepFactory: inject system prompt and guardrails as
	// transforms. The factory is invoked once per dequeued event
	// by the engine (via Build → stepFactory). The slash handlers
	// are bound to the session by Build before the step runs;
	// stdio doesn't intercept slash commands and skips the
	// bindings implicitly (no Build runs for stdio events).
	stepFactory := func(sess *session.Session) ([]loop.Option, error) {
		// Set up progressive skill discovery. Built-in skills are
		// authoritative on name collision — passed first so the framework's
		// defaults (ore's writing-skills) and workshop's own sub-agent
		// authoring guidance (subagent-authoring) win over any skill the
		// user has dropped into .agents/skills or ~/.agents/skills.
		// Composition pattern: x/tool/skills/doc.go.
		var discoverers []skills.Discoverer
		discoverers = append(discoverers, skills.BuiltInSkills)                     // ore: writing-skills
		discoverers = append(discoverers, subagent.BuiltInSkills)                   // workshop: subagent-authoring
		discoverers = append(discoverers, skills.NewFSDiscoverer(".agents/skills")) // repo-local
		if homeDir, err := os.UserHomeDir(); err == nil {
			discoverers = append(discoverers, skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills"))) // user-global
		}
		skillsToolkit := skills.NewToolkit(discoverers...)

		sp, err := makeSystemPromptTransform(cfg, skillsToolkit)
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

		registry := tool.NewRegistry()

		wsSandbox := &workshopSandbox{name: "workshop", mr: sess}

		if sbr, ok := registry.(tool.SandboxRegistry); ok {
			sbr.SetDefaultSandbox(wsSandbox)
		}

		if err := skillsToolkit.Register(registry); err != nil {
			return nil, fmt.Errorf("register skills toolkit: %w", err)
		}

		parentPairs, err := registerWorkshopTools(registry, sess, cfg.defaultProviderConfig())
		if err != nil {
			return nil, fmt.Errorf("register workshop tools: %w", err)
		}

		subs, err := subagent.ListSubagentDefinitions(subagent.Dir(), nil)
		if err != nil {
			return nil, fmt.Errorf("list subagents: %w", err)
		}

		registered := make(map[string]bool, len(parentPairs))
		for _, t := range registry.Tools() {
			registered[t.Name] = true
		}

		parentFuncs := make(map[string]tool.ToolFunc, len(parentPairs))
		for name, p := range parentPairs {
			parentFuncs[name] = p.Func
		}

		for _, sa := range subs {
			if registered[sa.Name] {
				return nil, fmt.Errorf("subagent %q collides with already-registered tool", sa.Name)
			}
			saTool, saFn := buildSubagentTool(sa, prov, defaultSpec,
				registry.Tools(), parentFuncs, wsSandbox, tracer,
				cfg.defaultProviderConfig().Kind)
			mustRegister(registry, saTool, saFn)
			registered[sa.Name] = true
		}

		invokeOpts := buildInvokeOptions(cfg, registry.Tools())

		tel := telemetry.New(meter)

		return []loop.Option{
			loop.WithTransforms(sp, compaction.NewTransform(), gr),
			loop.WithHandlers(xtool.NewHandler(registry, xtool.WithTracer(tracer)), usage.New()),
			loop.WithInvokeOptions(invokeOpts...),
			loop.WithDefaultSpec(defaultSpec),
			loop.WithTracer(tracer),
			loop.WithOnEmit(tel.OnEmit()),
		}, nil
	}

	handlers := slashHandlers{cc, tc, ac}
	factory := &tuiEngineFactory{
		stepFactory: stepFactory,
		prov:        prov,
		defaultSpec: defaultSpec,
		tracer:      tracer,
		slashReg:    slashReg,
		handlers:    handlers,
	}

	registry := session.NewInMemoryRegistry()
	eng, err := engine.New(registry, factory)
	if err != nil {
		return nil, fmt.Errorf("create engine: %w", err)
	}

	return &sessionSetup{
		cfg:         cfg,
		repo:        repo,
		registry:    registry,
		engine:      eng,
		factory:     factory,
		slashReg:    slashReg,
		handlers:    handlers,
		defaultSpec: defaultSpec,
		meter:       meter,
	}, nil
}

// newSession constructs a fresh ephemeral *session.Session with a
// new UUID and an empty ledger thread. The caller is responsible
// for registering the session in s.registry before driving it.
func (s *sessionSetup) newSession() *session.Session {
	id := generateThreadID()
	thread := ledger.NewThread()
	return session.New(id, thread)
}

// attachSession hydrates a thread from the durable store by ID and
// constructs a *session.Session wrapping it. Returns an error when
// the thread cannot be hydrated (the underlying HydrateThread
// reports the cause).
func (s *sessionSetup) attachSession(ctx context.Context, threadID string) (*session.Session, error) {
	turns, tip, err := s.repo.HydrateThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("hydrate thread %s: %w", threadID, err)
	}
	thread := ledger.NewThread()
	for _, t := range turns {
		thread.SaveTurn(t)
	}
	thread.SetCurrentTip(tip)
	return session.New(threadID, thread), nil
}

// seedMetadata writes static info-bar keys into the
// session's live metadata store. These power the TUI status zone
// (read via sess.AllMetadata()) and are emitted on every SetMetadata call as a
// PropertiesEvent.
//
// The keys are thread_id, cwd (with home-prefix shortened to ~), git_branch
// (or "(not in git repo)"), tui.pid, and model (from defaultSpec.Name).
func (s *sessionSetup) seedMetadata(sess *session.Session) {
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

	sess.SetMetadata("thread_id", sess.ID())
	sess.SetMetadata("cwd", shortCwd)
	sess.SetMetadata("git_branch", branch)

	sess.SetMetadata("tui.pid", strconv.Itoa(os.Getpid()))

	if s.defaultSpec.Name != "" {
		sess.SetMetadata("model", s.defaultSpec.Name)
	}
}

// makeSystemPromptTransform builds the composable system prompt transform. It
// concatenates the default prompt, working-directory context, available skills,
// repository instructions, and runtime details.
func makeSystemPromptTransform(cfg *config, skillsToolkit *skills.Toolkit) (loop.Transform, error) {
	return systemprompt.New(
		systemprompt.WithContentFunc(func() string { return defaultPrompt }),
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
	if pc.CacheControl != "" {
		// pc.CacheControl is a Go duration string (e.g. "5m",
		// "1h"). Any value time.ParseDuration accepts is
		// forwarded; the framework's canonical 5m / 1h constants
		// are documented but not enforced here. Values that fail
		// to parse, or that parse to 0, are dropped silently —
		// buildDefaultSpec is not the place to fail loudly. A
		// future validation pass in loadProvidersConfig could
		// reject malformed values at config-load time (out of
		// scope here).
		if d, err := time.ParseDuration(pc.CacheControl); err == nil && d != 0 {
			spec.CacheControl = &models.CacheControl{TTL: d}
		}
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
	case "codex":
		opts = append(opts, codex.WithTools(tools))
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
	case "codex":
		if pc.Model == "" {
			return nil, fmt.Errorf("missing required provider config: model")
		}
		opts := []codex.Option{codex.WithOriginator("workshop")}
		if tracer != nil {
			opts = append(opts, codex.WithTracer(tracer))
		}
		inner, err := codex.New(opts...)
		if err != nil {
			return nil, err
		}
		if !inner.LoggedIn() {
			return nil, fmt.Errorf("codex is not logged in; run `workshop auth login`")
		}
		return wrapWithRetry(&codexCompatibilityProvider{inner: inner}, tracer), nil
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
//   - Every defined name must have a non-empty model.
//   - OpenAI and Anthropic providers must have a non-empty api-key; Codex uses
//     separately persisted ChatGPT credentials instead.
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
// tool.Tool struct.
func mustRegisterRaw(registry tool.Registry, name, description string, schema map[string]any, fn tool.ToolFunc) {
	if err := registry.Register(tool.Tool{Name: name, Description: description, Schema: schema}, fn); err != nil {
		panic(fmt.Sprintf("register %s: %v", name, err))
	}
}

// defaultPrompt is the baked-in system prompt.
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
