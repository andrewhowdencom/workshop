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
// would have applied (transforms, handlers, spec, tracer, on-emit
// callbacks) — and starts a bridge goroutine that forwards every
// emission on the per-turn step to the session's emitter. The
// session's bound state handles the append exactly once: the user
// turn is added by the engine via sess.Submit (session step's
// auto-append), and the assistant turn is added by the bridge (also
// session step's auto-append). No double-append.
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
)

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
type tuiEngineFactory struct {
	mgr         *junk.Manager
	stepFactory func(*junk.Stream) ([]loop.Option, error)
	prov        provider.Provider
	defaultSpec models.Spec
	tracer      trace.Tracer
}

// Build implements agent.Factory. It looks up the *junk.Stream backing
// the session, calls the workshop's stepFactory to obtain the
// configured loop.Options, and constructs a dedicated per-turn step
// from those options. The step is intentionally NOT state-bound (see
// the package docstring on double-append avoidance); a bridge
// goroutine forwards emissions from this step to the session for
// both subscriber fanout (the TUI) and state auto-append
// (TurnCompleteEvent → bound thread).
//
// The bridge goroutine is owned by the per-turn step: it closes the
// step on exit, and the engine's per-event lifecycle ensures only one
// bridge runs per active turn. See bridgeStepToSession for the
// lifecycle contract.
func (f *tuiEngineFactory) Build(sess *session.Session) (*agent.Agent, error) {
	stream, err := f.mgr.Get(sess.ID())
	if err != nil {
		return nil, fmt.Errorf("engine: lookup stream for session %s: %w", sess.ID(), err)
	}

	opts, err := f.stepFactory(stream)
	if err != nil {
		return nil, fmt.Errorf("engine: build step options: %w", err)
	}

	// The per-turn step carries all the workshop's transforms,
	// handlers, spec, tracer, and on-emit callbacks — but is NOT
	// bound to the session's thread. The session's step (created
	// by session.New) is the only state-bound step, and the
	// bridge below routes every emission here so that step's
	// auto-append runs exactly once.
	step := loop.New(opts...)

	go bridgeStepToSession(step, sess)

	return agent.New(sess.ID(),
		agent.WithProvider(f.prov),
		agent.WithSpec(f.defaultSpec),
		agent.WithPattern(&cognitive.ReAct{}),
		agent.WithTracer(f.tracer),
		agent.WithStep(step),
	), nil
}

// bridgeStepToSession forwards every event emitted on src to the
// destination session's emitter. It is a single-pass pump: it
// subscribes to src, ranges over the channel until it closes, and
// closes src on exit so the per-turn step's resources are released.
//
// ctx is used only for the Emit calls. Background is appropriate
// here because the per-turn context may be cancelled by the engine
// when the parent context is cancelled; the bridge should still
// drain in-flight emissions so the session sees a coherent event
// stream.
//
// Why we don't use sess.Submit on the bridge path: sess.Submit runs
// the pipeline handlers and emits a fresh TurnCompleteEvent. The
// per-turn step has already emitted a TurnCompleteEvent with the
// assistant turn; calling sess.Submit would re-emit it (double emit
// to subscribers) and trigger double auto-append. Forwarding the
// raw event through the session's emitter preserves the canonical
// "single emit, single append" invariant.
func bridgeStepToSession(src *loop.Step, sess *session.Session) {
	defer src.Close()
	out := src.Subscribe(
		"text_delta", "reasoning_delta", "tool_call", "tool_result",
		"turn_complete", "error", "properties", "lifecycle", "notice", "activity",
	)
	for event := range out {
		sess.Emitter().Emit(context.Background(), event)
	}
}

// runTUIEngine wires the TUI's session into an engine.Engine,
// forwards the TUI's outbound Events() channel into engine.Submit,
// and persists the thread after every turn.
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
	stream *junk.Stream,
) error {
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

	// 1. Event pump: forward every session.Event from the TUI to
	// engine.Submit. The TUI emits session.UserMessageEvent when
	// the user presses Enter and session.InterruptEvent when the
	// user presses Ctrl+C / Esc. engine.Submit returns an error
	// only on session-not-found or queue-full; either is fatal to
	// the pump.
	events := tuiConduit.Events()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for evt := range events {
			if err := eng.Submit(ctx, sess.ID(), evt); err != nil {
				slog.Error("engine.Submit failed; stopping pump", "err", err)
				return
			}
		}
	}()

	// 2. Persistence pump: best-effort save after every lifecycle
	// "done" event the engine emits (one per handled event on
	// success). Pre-bump, junk.Manager's worker persisted on every
	// turn; this restores that behavior. The save is best-effort
	// because failing to persist is not a fatal error for an
	// interactive TUI session — the user can retry by typing.
	lifecycleDone := make(chan struct{})
	go func() {
		defer close(lifecycleDone)
		for event := range sess.Subscribe("lifecycle") {
			le, ok := event.(loop.LifecycleEvent)
			if !ok || le.Phase != "done" {
				continue
			}
			if err := stream.Save(); err != nil {
				slog.Warn("save thread failed", "err", err)
			}
		}
	}()

	// 3. Start the TUI. Blocks until ctx is cancelled, the user
	// presses Ctrl+C, or the Bubble Tea program errors. tui.Start
	// closes the Events() channel on return.
	startErr := tuiConduit.Start(ctx)

	// 4. Drain the pumps. By this point the Events() channel is
	// closed (tui.Start closes it on return), so pumpDone closes
	// once the for-range loop exits. lifecycleDone closes only
	// when the session is closed; cancelling the context and
	// closing the engine drains the lifecycle subscription.
	<-pumpDone
	<-lifecycleDone

	return startErr
}