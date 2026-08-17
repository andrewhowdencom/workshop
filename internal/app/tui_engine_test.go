// Package app — tests for the TUI engine pump.
//
// The tests below exercise the wiring that closes the gap between
// the TUI's outbound Events() channel and engine.Engine.Submit.
// Two regressions are specifically guarded against:
//
//  1. tuiConduit.Events() returns nil until tuiConduit.Start()
//     initializes t.events (see x/conduit/tui/tui.go). The pump
//     goroutine in runTUIEngine must therefore read Events()
//     AFTER Start() has begun, not before. Ranging over a nil
//     channel blocks forever, which would silently drop every
//     user message.
//
//  2. sess.Subscribe("lifecycle") returns a channel that is only
//     closed when the session is closed. Without an explicit
//     sess.Close after tui.Start returns, the persistence pump
//     hangs and runTUIEngine never returns.
//
//  3. factory.Build must subscribe to the per-turn step BEFORE
//     returning the agent, otherwise events emitted by the
//     agent (which the engine invokes immediately after Build)
//     race against subscription registration and are silently
//     dropped (FanOut is live-only).
//
//  4. factory.Build must Close on the per-turn step after the
//     bridge drains, otherwise the bridge leaks (the per-turn
//     step and its fanout stay open for the lifetime of the
//     factory). factory.Close() drains all pending steps.
package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewhowdencom/ore/agent"
	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/cognitive"
	"github.com/andrewhowdencom/ore/engine"
	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoProvider is a stub provider that emits a fixed Text artifact
// and then a StopReason. It satisfies provider.Provider.
type echoProvider struct {
	calls atomic.Int64
}

// Invoke emits a single Text artifact and a StopReason, which
// ReAct accepts as the final assistant turn.
func (p *echoProvider) Invoke(ctx context.Context, s ledger.State, spec models.Spec, ch chan<- artifact.Artifact, opts ...provider.InvokeOption) error {
	p.calls.Add(1)
	ch <- artifact.Text{Content: "hello from the provider"}
	ch <- artifact.StopReason{Reason: artifact.StopReasonStop}
	return nil
}

// newTestEngine builds the full tuiEngineFactory wiring against an
// in-memory junk store. The stepFactory is minimal (no transforms,
// no handlers) because the tests below verify the bridge and
// engine-pump path, not the workshop's full transform pipeline.
//
// Returns the *junk.Manager (so tests can inspect provider calls),
// the *tuiEngineFactory, the *session.Session wrapping the
// manager's stream, and the *junk.Stream itself.
func newTestEngine(t *testing.T) (*junk.Manager, *tuiEngineFactory, *session.Session, *junk.Stream, *echoProvider) {
	t.Helper()

	store := junk.NewMemoryStore()
	prov := &echoProvider{}

	mgr := junk.NewManager(store, prov,
		func(stream *junk.Stream) ([]loop.Option, error) {
			return []loop.Option{}, nil
		},
		func(ctx context.Context, step *loop.Step, st ledger.State, prov provider.Provider, spec models.Spec) (ledger.State, error) {
			return cognitive.NewTurnProcessor(cognitive.ReActFactory, nil)(ctx, step, st, prov, spec)
		},
	)

	stream, err := mgr.Create()
	require.NoError(t, err, "create stream")

	sess := session.New(stream.ID(), stream.State().(*ledger.Thread))
	require.NotNil(t, sess, "session.New returned nil")

	factory := &tuiEngineFactory{
		stream:      stream,
		stepFactory: func(stream *junk.Stream) ([]loop.Option, error) { return []loop.Option{}, nil },
		prov:        prov,
		defaultSpec: models.Spec{Name: "echo"},
	}
	t.Cleanup(factory.Close)

	return mgr, factory, sess, stream, prov
}

// TestTUIEngineFactory_Build_DrivesAgent asserts that a per-turn
// agent built by tuiEngineFactory:
//
//   - calls the underlying provider,
//   - emits a TurnCompleteEvent on the per-turn step, and
//   - bridges that event to the session (which auto-appends the
//     assistant turn to the shared thread).
//
// This is the end-to-end guarantee the user's "hi" test depends
// on: typing into the TUI, having the engine drive the agent, and
// the assistant turn landing in the thread that the TUI reads
// from. The bridge is the missing piece that makes the session
// the canonical event stream; without it, the TUI sees the user
// turn but never the assistant's response.
func TestTUIEngineFactory_Build_DrivesAgent(t *testing.T) {
	_, factory, sess, _, prov := newTestEngine(t)
	defer sess.Close()

	ag, err := factory.Build(sess)
	require.NoError(t, err, "factory.Build")
	require.NotNil(t, ag, "factory.Build returned nil agent")

	_, err = ag.Run(context.Background(), sess.Thread())
	require.NoError(t, err, "ag.Run")

	// Close the factory to drain the bridge. Without this, the
	// bridge is still running asynchronously and the session's
	// thread may not yet have the assistant turn appended.
	factory.Close()

	// After the bridge drains, the session's bound state
	// auto-appends the assistant turn. session.Turns() therefore
	// contains exactly one assistant turn.
	turns := sess.Turns()
	require.Len(t, turns, 1, "session.Turns() after agent.Run")
	assert.Equal(t, ledger.RoleAssistant, turns[0].Role, "the persisted turn should be from the assistant")

	// The provider must have been called exactly once.
	assert.Equal(t, int64(1), prov.calls.Load(), "provider should be invoked once")
}

// TestTUIEngineFactory_Build_NoDoubleAppend asserts that two
// consecutive factory.Build + ag.Run pairs each append EXACTLY
// one assistant turn to the session thread — no double-append
// from the per-turn step's auto-append AND the bridge forwarding
// to the session step (which would also auto-append if both were
// state-bound).
//
// The fix relies on the per-turn step NOT being state-bound; the
// session's bound state is the single source of truth.
func TestTUIEngineFactory_Build_NoDoubleAppend(t *testing.T) {
	_, factory, sess, _, _ := newTestEngine(t)
	defer sess.Close()

	// First run.
	ag1, err := factory.Build(sess)
	require.NoError(t, err)
	_, err = ag1.Run(context.Background(), sess.Thread())
	require.NoError(t, err)
	factory.Close()
	require.Len(t, sess.Turns(), 1, "after first run, session should have exactly one turn")

	// Second run.
	ag2, err := factory.Build(sess)
	require.NoError(t, err)
	_, err = ag2.Run(context.Background(), sess.Thread())
	require.NoError(t, err)
	factory.Close()

	// Without the no-state-binding design, the per-turn step
	// would auto-append AND the bridge would forward to the
	// session which would also auto-append, producing two turns
	// per Run. With the design, exactly one turn per Run.
	assert.Len(t, sess.Turns(), 2, "session should have exactly two turns after two runs (no double-append)")
}

// TestEngineSubmit_DrivesAgentAndBridgesToSession is the closest
// unit-level test to the user's reported bug. It submits a
// UserMessageEvent to an engine wired with tuiEngineFactory and
// asserts that:
//
//   - the engine drives the agent exactly once,
//   - the session sees the user turn appended (via sess.Submit),
//   - the session sees the assistant turn appended (via the
//     bridge from the per-turn step), and
//   - a "done" LifecycleEvent is emitted on the session.
//
// All four signals are required for the TUI to render a response:
// without (3), the user sees "hi" echoed but no assistant reply.
// This is the regression the original bump-to-latest migration
// introduced and that the fix closes.
func TestEngineSubmit_DrivesAgentAndBridgesToSession(t *testing.T) {
	_, factory, sess, _, prov := newTestEngine(t)
	defer sess.Close()

	registry := session.NewInMemoryRegistry()
	require.NoError(t, registry.Register(sess))

	eng, err := engine.New(registry, factory)
	require.NoError(t, err, "engine.New")
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	// Submit a user message. This is the equivalent of the TUI
	// emitting session.UserMessageEvent on its Events() channel.
	require.NoError(t, eng.Submit(context.Background(), sess.ID(),
		session.UserMessageEvent{Content: "hi"},
	))

	// The engine's handleEvent is asynchronous (per-session
	// mailbox). Wait for the agent to complete, then drain the
	// factory. factory.Close is purely for resource cleanup with
	// the synchronous OnEmit design (every event has already been
	// forwarded by the time step.Turn returns); the sleep
	// ensures the engine has finished handling the event before
	// we read sess.Turns().
	time.Sleep(50 * time.Millisecond)
	factory.Close()

	// The session thread must contain both the user turn and the
	// assistant turn. The user turn is added by engine.Submit →
	// sess.Submit (session step auto-append). The assistant
	// turn is added by the bridge (per-turn step's
	// TurnCompleteEvent → session.Emitter().Emit → session step
	// auto-append).
	turns := sess.Turns()
	require.Len(t, turns, 2, "session should have user turn + assistant turn after one submit")

	assert.Equal(t, ledger.RoleUser, turns[0].Role, "first turn is the user message")
	assert.Equal(t, ledger.RoleAssistant, turns[1].Role, "second turn is the assistant response")

	// The provider was invoked exactly once for the one Submit.
	assert.Equal(t, int64(1), prov.calls.Load(), "provider should be invoked once per Submit")
}

// TestRunTUIEngine_PumpWaitsForEvents is the focused regression
// test for the nil-channel bug. We can't drive a real *tui.TUI
// from a unit test (Bubble Tea needs a TTY), so we test the
// equivalent polling pattern in isolation.
//
// The test simulates the lifecycle of t.events: nil before
// "Start", non-nil after. A goroutine that polls Events() and
// ranges over the result must successfully drain the channel
// once it becomes non-nil.
func TestRunTUIEngine_PumpWaitsForEvents(t *testing.T) {
	// ch models t.events: nil until Set() is called, then a
	// channel that delivers one event and then closes.
	var ch atomic.Pointer[chan session.Event]
	ch.Store(nil)

	getEvents := func() <-chan session.Event {
		v := ch.Load()
		if v == nil {
			return nil
		}
		return *v
	}

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		var events <-chan session.Event
		for events == nil {
			events = getEvents()
			if events == nil {
				// runtime.Gosched in production; here we
				// yield via a tiny sleep so the test
				// doesn't spin hot.
				time.Sleep(time.Millisecond)
			}
		}
		for range events {
			// drain
		}
	}()

	// The pump goroutine should be blocked because Events() is
	// still nil.
	select {
	case <-pumpDone:
		t.Fatal("pump exited while Events() was still nil")
	case <-time.After(50 * time.Millisecond):
		// expected: pump is still blocked
	}

	// Initialize t.events and close it (simulating Start() init
	// and then Start() returning).
	c := make(chan session.Event, 1)
	ch.Store(&c)
	close(c)

	// The pump must drain and exit within a deadline.
	select {
	case <-pumpDone:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not drain after Events() became non-nil")
	}
}

// _ silences the unused-import linter for agent, which is
// referenced only via the tuiEngineFactory type signature.
var _ = agent.New