// Package app — TUI engine pump and per-session agent factory.
//
// # Background
//
// As of ore v1.x the TUI conduit (x/conduit/tui) follows the
// session-based contract documented in x/conduit/doc.go:
//
//   - tui.New(sess *session.Session, opts ...) (was: tui.New(mgr *junk.Manager, ...))
//
// The TUI is a "dumb pipe" in this contract: it accepts user input and
// emits session.Event values on a buffered Events() channel that the
// application is responsible for consuming. The session's own
// loop.Step (created by session.New with WithState(thread)) is a bare
// step used for subscriber fanout; it is not configured with the
// workshop's transforms, handlers, or spec, and the TUI does not own
// the inference loop.
//
// # Why this file exists
//
// The previous bump-to-latest migration (commit 26ebace, "Bump all
// direct deps to latest; adapt to new tui/http API") wired the TUI to
// a *session.Session via the junkBackend adapter but left the TUI's
// Events() channel unwired: the package docstring on
// internal/app/backend.go explicitly noted that "the TUI's emitted
// channel is not yet pumped into an inference engine, so TUI
// submissions do not currently reach the manager's worker."
//
// That gap is closed here: tuiEngineFactory builds per-turn agents
// whose emissions are bridged into the session, and runTUIEngine pumps
// the TUI's outbound events through an engine.Engine. The HTTP and
// stdio conduits are unchanged — stdio continues to use junk.Manager
// directly (it predates the engine migration), and HTTP is
// request-driven (no continuous event loop needed).
//
// # Per-turn step, not session step
//
// The session's own step is hardcoded by session.New to a step bound
// to the thread (loop.WithState(thread)). Binding the per-turn agent's
// step to the same thread would cause double-append on every
// TurnCompleteEvent: the agent's step would auto-append, the session's
// step would auto-append, and the bridge forward would emit yet again.
//
// To avoid that, the factory builds the per-turn step WITHOUT
// WithState — using the same loop.Options that junk.Manager's worker
// would have applied (transforms, handlers, spec, tracer) — and
// registers a synchronous OnEmit callback that forwards every
// emission to the session's emitter. The OnEmit runs inline inside
// EventBus.Emit, so by the time step.Turn returns the session's
// bound state has appended the assistant turn. No double-append,
// no race between the bridge and the pattern.
//
// Synchronous forwarding is load-bearing. An asynchronous bridge
// (Subscribe + goroutine) would race with the pattern's check of
// last-role-in-state; ReAct would call step.Turn multiple times for
// one user message, producing duplicate assistant turns. The OnEmit
// approach closes that race by serializing the forward with Emit.
//
// # Persistence
//
// Pre-bump, junk.Manager's worker persisted the thread on every turn.
// Post-bump and pre-this-fix, nothing persisted. runTUIEngine restores
// that behavior by calling stream.Save() on every LifecycleEvent
// "done" the engine emits. Save is best-effort; failures are logged
// at warn level.
//
// # Slash commands (deferred)
//
// Slash commands (junk.WithInterceptor was removed by ore v1.0) are
// not yet wired in the TUI path. The slash registry's Intercept
// method is exposed but the workshop's conduits still do not call it
// before Submit. This is the same deferral called out in
// internal/app/backend.go; this fix does not change that.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"github.com/andrewhowdencom/ore/agent"
	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/engine"
	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/models"
	"go.opentelemetry.io/otel/trace"

	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/x/conduit/tui"
	slash "github.com/andrewhowdencom/ore/x/slash"
)

// slashHandler is the narrow interface the factory needs from
// each slash command: the ability to bind to a session. The
// slash.Handler interface (Handle) is implemented separately and
// bound to slashReg in buildManager; this is just the session-bind
// surface the factory uses on every Build.
type slashHandler interface {
	SetSession(sess *session.Session)
}

// findHandler returns the first handler in hs whose concrete type
// is T. Used by the factory to call type-specific methods like
// compactCommand.SetStream. Returns (handler, true) on a match;
// (zero, false) if no handler of that type is registered.
func findHandler[T slashHandler](hs slashHandlers) (T, bool) {
	var zero T
	for _, h := range hs {
		if v, ok := h.(T); ok {
			return v, true
		}
	}
	return zero, false
}

// slashHandlers is a slice of slashHandler, bound as a group on
// every factory Build call.
type slashHandlers []slashHandler

// tuiEngineFactory is the per-session agent.Factory that builds
// agents for the TUI's session-based inference path.
//
// The factory owns the dependencies the agent bundle needs (provider,
// default spec, tracer) and the workshop's existing stepFactory so
// the per-turn step carries the same transforms, handlers, and
// metadata as the junk.Manager-driven worker did pre-bump.
//
// Build is invoked once per dequeued event by engine.Engine. Each
// call produces a fresh agent with a fresh loop.Step; the agent is
// discarded when the engine moves on to the next event.
//
// Per-turn steps are tracked so the factory can release them on
// Close. The engine does not know about the steps — it only sees
// *agent.Agent — so the factory must own the lifecycle. Callers
// (runTUIEngine, the test suite) must invoke Close when done to
// release the per-turn step resources; see Close.
//
// The factory also owns the slash registry (so the runTUIEngine pump
// can call slashReg.Intercept before submitting) and the slash
// handlers (so it can bind them to the session on every Build via
// SetSession). The handlers live here rather than in a global so
// their lifetime is tied to the TUI session's lifetime.
//
// The stream field carries the *junk.Stream that backs the session.
// It is set at factory construction by the caller (see
// buildManager / RunTUI) and used in two places:
//
//  1. Build calls f.stream.SetMetadata (via compactCommand) so the
//     boundary info it writes persists across turns.
//  2. SaveThread exposes the stream's Save for the lifecycle
//     persistence pump; this is what Task 3 of the kill-junk
//     migration retains so persistence works until Task 4 replaces
//     it with ledger.Repository.SaveTurn.
//
// In Task 4 the stream field is removed entirely and the factory
// takes a saveFn callback instead.
type tuiEngineFactory struct {
	stream      *junk.Stream
	stepFactory func(*junk.Stream) ([]loop.Option, error)
	prov        provider.Provider
	defaultSpec models.Spec
	tracer      trace.Tracer
	slashReg    slash.Registry
	handlers    slashHandlers

	// mu guards pending. Build appends to pending; Close reads and
	// clears pending. Build calls are concurrent across sessions
	// (the engine serializes per-session via its mailbox), so mu
	// is necessary.
	mu      sync.Mutex
	pending []*loop.Step
}

// Build implements agent.Factory. It looks up the *junk.Stream backing
// the session, calls the workshop's stepFactory to obtain the
// configured loop.Options, and constructs a dedicated per-turn step
// from those options.
//
// The step is intentionally NOT state-bound (see the package
// docstring on double-append avoidance). Instead, an OnEmit callback
// is registered that synchronously forwards every emission to the
// session's emitter. Synchronous forwarding is load-bearing: the
// pattern (ReAct) reads the session thread on every iteration to
// decide whether to loop again, so the assistant turn produced by
// step.Turn MUST be appended to the thread before that read. An
// asynchronous bridge (Subscribe + goroutine) races with the pattern
// and causes ReAct to call step.Turn multiple times for a single
// user message, producing N duplicates of the assistant turn. The
// OnEmit callback runs inside EventBus.Emit (synchronously, before
// the fanout send), so by the time step.Turn returns, the session
// has appended the assistant turn and the pattern sees it.
func (f *tuiEngineFactory) Build(sess *session.Session) (*agent.Agent, error) {
	// Bind slash handlers to the session before any agent code runs.
	// SetSession is idempotent — each handler caches the session
	// reference and seeds per-session state (e.g. roleCommand's
	// resolver path from session metadata). The TUI path goes
	// through here on every dequeued event; stdio doesn't
	// intercept slash commands today and skips this.
	for _, h := range f.handlers {
		h.SetSession(sess)
	}

	// Bind the stream to compactCommand so the boundary info it
	// writes survives junk.Stream.Save. Other handlers (role,
	// thinking, analytics) don't need the stream.
	if cc, ok := findHandler[*compactCommand](f.handlers); ok {
		cc.SetStream(f.stream)
	}

	opts, err := f.stepFactory(f.stream)
	if err != nil {
		return nil, fmt.Errorf("engine: build step options: %w", err)
	}

	// Append the synchronous bridge OnEmit to the stepFactory's
	// options. The OnEmit forwards every emission to the session's
	// emitter, which:
	//   - auto-appends TurnCompleteEvents to the session thread
	//     (via the session's bound state), and
	//   - fans the event out to the session's subscribers (the
	//     TUI's Subscribe loop).
	//
	// The OnEmit runs synchronously inside EventBus.Emit (see
	// x/loop/eventbus.go), so step.Turn does not return until
	// every emission has been forwarded.
	opts = append(opts, loop.WithOnEmit(func(ctx context.Context, event loop.OutputEvent) {
		sess.Emitter().Emit(ctx, event)
	}))

	step := loop.New(opts...)

	// Track the per-turn step so Close can drain its FanOut
	// subscribers (if any) at shutdown. With the synchronous
	// OnEmit design, the bridge is no longer a separate
	// goroutine — emissions are forwarded inline — so there is
	// nothing for the bridge to "drain". But step.Close still
	// closes the EventBus and is required for resource cleanup
	// (the FanOut's run goroutine, the buffered events channel,
	// etc.). The test suite and runTUIEngine both invoke Close
	// to release those resources.
	f.mu.Lock()
	f.pending = append(f.pending, step)
	f.mu.Unlock()

	return agent.New(sess.ID(),
		agent.WithProvider(f.prov),
		agent.WithSpec(f.defaultSpec),
		agent.WithPattern(&cognitive.ReAct{}),
		agent.WithTracer(f.tracer),
		agent.WithStep(step),
	), nil
}

// SetStream binds the *junk.Stream backing the session to the
// factory. It is called once per session, from RunTUI, after
// junkBackend.CreateSession resolves the stream. Build then uses
// f.stream for the compact handler's SetStream and the
// stepFactory. Persistence is driven by SessionPersister (Task 4)
// which subscribes directly to the session's TurnCompleteEvent;
// f.stream.Save is no longer called by the lifecycle pump.
//
// Note: the stream field is still needed in Task 4 because
// compactCommand's SetStream takes a *junk.Stream and the
// stepFactory closure takes a *junk.Stream. Both are slated for
// removal in Task 5 (slash handler and compact lose the stream
// field) and Task 7 (test fixtures rewrite).
func (f *tuiEngineFactory) SetStream(stream *junk.Stream) {
	f.stream = stream
}

// Close drains every pending per-turn step. Each step.Close closes
// its EventBus/FanOut, releasing the buffered events channel and
// stopping the FanOut's run goroutine. With the synchronous OnEmit
// design (see Build), every event has already been forwarded to
// the session by the time step.Turn returns; Close is purely for
// resource cleanup, not for waiting on in-flight bridges.
//
// Close is safe to call once; subsequent calls are no-ops because
// pending is cleared on the first call. Concurrent Build calls
// during Close are safe: a new Build appends to pending after
// Close has cleared it; that new step is the caller's
// responsibility to drain (e.g. by calling Close again).
//
// Production callers (runTUIEngine) call Close after the TUI
// exits. Test callers call Close via t.Cleanup to release the
// per-turn step resources.
func (f *tuiEngineFactory) Close() {
	f.mu.Lock()
	pending := f.pending
	f.pending = nil
	f.mu.Unlock()

	for _, p := range pending {
		_ = p.Close()
	}
}

// runTUIEngine wires the TUI's session into an engine.Engine,
// forwards the TUI's outbound Events() channel into engine.Submit,
// and persists the thread after every turn via the supplied
// SessionPersister.
//
// Returns the error from tuiConduit.Start (the TUI's blocking loop)
// once both the engine pump and the persistence pump have drained.
//
// Cancellation: the caller-provided ctx is shared between the
// engine's event submission (passed to eng.Submit) and the TUI's
// Start. A single cancellation unwinds the UI loop, any in-flight
// engine execution, and both pump goroutines.
func runTUIEngine(
	ctx context.Context,
	sess *session.Session,
	tuiConduit *tui.TUI,
	factory *tuiEngineFactory,
	persister SessionPersister,
) error {
	// Bind slash handlers to the session BEFORE the TUI starts so
	// slash commands (e.g. /role, /thinking) work on a fresh
	// TUI without requiring the user to send a chat message first.
	// The factory's Build also binds handlers, but Build only runs
	// when the engine processes an inference event — so without
	// this pre-bind, the user gets "no active session" on their
	// first /role attempt.
	for _, h := range factory.handlers {
		h.SetSession(sess)
	}

	reg := session.NewInMemoryRegistry()
	if err := reg.Register(sess); err != nil {
		return fmt.Errorf("register session: %w", err)
	}

	eng, err := engine.New(reg, factory)
	if err != nil {
		return fmt.Errorf("create engine: %w", err)
	}
	defer func() {
		// eng.Close drains active mailboxes; a background context
		// is used because the caller's ctx may already be
		// cancelled at this point.
		if err := eng.Close(context.Background()); err != nil {
			slog.Warn("engine close", "err", err)
		}
	}()

	// Persister: subscribe to the session's TurnCompleteEvent and
	// append a journal entry per turn. Replaces the legacy
	// junk.Stream.Save() snapshot path. The subscription is closed
	// when sess.Close below propagates through the session's step
	// into the subscription channel, letting the persister goroutine
	// drain cleanly.
	if err := persister.Attach(sess); err != nil {
		return fmt.Errorf("attach persister: %w", err)
	}
	defer func() {
		if err := persister.Close(); err != nil {
			slog.Warn("persister close", "err", err)
		}
	}()

	// 1. Event pump: forward every session.Event from the TUI to
	// engine.Submit. The TUI emits session.UserMessageEvent when
	// the user presses Enter and session.InterruptEvent when the
	// user presses Ctrl+C / Esc. engine.Submit returns an error
	// only on session-not-found or queue-full; either is fatal to
	// the pump.
	//
	// Slash interception: every event flows through
	// slashReg.Intercept before reaching the engine. The
	// interceptor matches against /<name> prefixes; matched
	// commands are consumed (no inference triggered) and any
	// notices (e.g. "Role: reviewer") are emitted on the
	// session's emitter so the user sees the feedback. Unmatched
	// events fall through unchanged. This is the ore v1.x
	// replacement for the removed junk.WithInterceptor; the
	// wiring lives in the application.
	//
	// tuiConduit.Events() returns nil until tuiConduit.Start()
	// initializes t.events (see x/conduit/tui/tui.go:333). The
	// pump goroutine must read Events() AFTER Start() has begun;
	// ranging over a nil channel blocks forever, which would
	// silently drop every user message. We poll Events() inside
	// the goroutine and yield via runtime.Gosched until it
	// returns a non-nil channel.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		var events <-chan session.Event
		for events == nil {
			events = tuiConduit.Events()
			if events == nil {
				runtime.Gosched()
			}
		}
		for evt := range events {
			result, err := factory.slashReg.Intercept(ctx, evt, sess, sess.Emitter())
			if err != nil {
				// The interceptor converts handler errors into
				// notices itself; reaching here means the
				// registry itself failed. Log and continue.
				slog.Warn("slash intercept failed", "err", err)
			}
			for _, n := range result.Notice {
				sess.Emitter().Emit(ctx, loop.NoticeEvent{Notice: n, Ctx: loop.WithProvenance(ctx, "tui")})
			}
			if result.Event == nil {
				// Slash handler consumed the event. Skip
				// engine.Submit; no LLM inference for this
				// turn. The persistence pump still picks up
				// the LifecycleEvent "done" emitted by
				// sess.Submit for the slash handler's
				// state-changing side effects.
				continue
			}
			if err := eng.Submit(ctx, sess.ID(), result.Event); err != nil {
				slog.Error("engine.Submit failed; stopping pump", "err", err)
				return
			}
		}
	}()

	// 3. Start the TUI. Blocks until ctx is cancelled, the user
	// presses Ctrl+C, or the Bubble Tea program errors. tui.Start
	// closes t.events on return, which lets the pump goroutine
	// drain (its for-range over Events() exits when the channel
	// closes).
	startErr := tuiConduit.Start(ctx)

	// 4. Close the session. This releases every Subscribe-based
	// channel (the persister goroutine above, and any TUI-side
	// subscriptions that survived tui.Start's exit), letting both
	// pumpDone and the persister goroutine close cleanly.
	_ = sess.Close()

	// 5. Release the TUI engine factory's per-turn steps. With the
	// synchronous OnEmit design (see tuiEngineFactory.Build), every
	// event has already been forwarded to the session by the time
	// step.Turn returned; Close is purely for resource cleanup
	// (EventBus/FanOut channels, run goroutines).
	factory.Close()

	// 6. Drain the pump. pumpDone closes when t.events is closed
	// (already happened inside tui.Start) and the for-range exits.
	<-pumpDone

	return startErr
}