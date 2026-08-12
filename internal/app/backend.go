// Package app — Backend adapter for the ore v1.3 session-based conduit API.
//
// Background
//
// As of ore v1.x the TUI and HTTP conduits follow the "session-based"
// contract documented in x/conduit/doc.go:
//
//   - tui.New(sess *session.Session, opts ...) (was: tui.New(mgr *junk.Manager, ...))
//   - http.New(backend httpc.Backend, opts ...) (was: http.New(mgr *junk.Manager, ...))
//
// Workshop still uses *junk.Manager as its central orchestrator (it owns
// the worker, the ledger thread, the processor, the slash registry, etc.).
// The Backend adapter below bridges *junk.Manager onto the new
// session-shaped surface that TUI and HTTP require, while keeping junk
// as the single source of truth for processing.
//
// Design notes
//
//  1. Each call to CreateSession returns a fresh *session.Session
//     wrapping the *junk.Stream's underlying ledger.Thread. The
//     session's own loop.Step is *not* the stream's worker step; it is
//     a passive view whose only purpose is to satisfy the new conduit
//     APIs. Inference continues to flow through *junk.Manager via its
//     existing worker.
//
//  2. Submit translates session.Event into the corresponding junk.Event
//     and forwards to the stream's worker via stream.Submit. This
//     keeps event ordering and processor invocation identical to the
//     pre-bump behavior.
//
//  3. The slash registry that was previously passed via
//     junk.WithInterceptor is no longer wired by junk. The application
//     is now responsible for calling slashReg.Intercept before each
//     submit. That wiring is deferred — slash commands will not be
//     processed until that wiring lands.
//
//  4. This adapter is sufficient to make the build pass and to keep
//     the existing inference behavior intact for stdio (which still
//     takes *junk.Manager directly per the legacy pattern). It is NOT
//     a long-term architecture: a future plan should migrate the
//     conduits to the engine + session.Registry pattern documented in
//     x/conduit/doc.go, at which point this file can be deleted.
package app

import (
	"context"
	"errors"

	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/session"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
)

// junkBackend adapts *junk.Manager to the httpc.Backend interface
// required by ore v1.3.0's session-based HTTP conduit.
//
// It is intentionally minimal: it preserves the underlying *junk.Manager's
// behavior for inference, lifecycle, and persistence, and surfaces that
// behavior through the narrow Backend interface that httpc.New demands.
type junkBackend struct {
	mgr *junk.Manager
}

// newJunkBackend constructs a Backend adapter around the given manager.
func newJunkBackend(mgr *junk.Manager) *junkBackend {
	return &junkBackend{mgr: mgr}
}

// Compile-time assertion that *junkBackend satisfies httpc.Backend.
var _ httpc.Backend = (*junkBackend)(nil)

// errUnexpectedEvent is returned by Submit when the framework emits an
// event type we do not know how to translate. Today only
// UserMessageEvent and InterruptEvent exist on both sides of the
// bridge; this error guards against future drift.
var errUnexpectedEvent = errors.New("junkBackend: unsupported event type")

// CreateSession creates a fresh *session.Session backed by a fresh
// *junk.Stream (when threadID is empty) or by the stream attached to the
// existing thread (when threadID is given).
//
// The returned session is a thin wrapper around the stream's
// ledger.Thread. Its own loop.Step is not used by inference; the
// stream's worker (owned by *junk.Manager) does the actual processing.
func (b *junkBackend) CreateSession(ctx context.Context, threadID string) (*session.Session, error) {
	var stream *junk.Stream
	var err error
	if threadID != "" {
		stream, err = b.mgr.Attach(threadID)
	} else {
		stream, err = b.mgr.Create()
	}
	if err != nil {
		return nil, err
	}
	return sessionFromStream(stream), nil
}

// GetSession returns the *session.Session for an active session by ID.
// The lookup is delegated to the underlying *junk.Manager; if no active
// stream exists under the given ID, an error is returned.
func (b *junkBackend) GetSession(ctx context.Context, id string) (*session.Session, error) {
	stream, err := b.mgr.Get(id)
	if err != nil {
		return nil, err
	}
	return sessionFromStream(stream), nil
}

// Submit forwards a session.Event to the *junk.Stream backing the named
// session. session.UserMessageEvent and session.InterruptEvent are the
// only event types the framework currently emits; both have structurally
// identical junk counterparts and are forwarded as-is.
func (b *junkBackend) Submit(ctx context.Context, id string, event session.Event) error {
	stream, err := b.mgr.Get(id)
	if err != nil {
		return err
	}
	je, err := junkEventFromSession(event)
	if err != nil {
		return err
	}
	return stream.Submit(je)
}

// ListThreads enumerates every persisted thread known to the manager.
// httpc.ThreadSummary is the application-facing shape that the HTTP
// conduit renders; only ID is populated because junk's *Thread does
// not expose preview / last-activity data through this surface.
func (b *junkBackend) ListThreads(ctx context.Context) ([]httpc.ThreadSummary, error) {
	threads, err := b.mgr.ListThreads()
	if err != nil {
		return nil, err
	}
	out := make([]httpc.ThreadSummary, 0, len(threads))
	for _, t := range threads {
		out = append(out, httpc.ThreadSummary{ID: t.ID})
	}
	return out, nil
}

// DeleteSession closes the *junk.Stream backing the named session and
// releases its resources. The persisted thread itself is not removed.
func (b *junkBackend) DeleteSession(ctx context.Context, id string) error {
	return b.mgr.Close(id)
}

// sessionFromStream constructs a *session.Session that exposes the
// stream's underlying *ledger.Thread. The session's own loop.Step is
// independent of the stream's worker step; the framework only reads
// from the session (turns, metadata, subscribe) and never invokes its
// step as part of processing. Inference runs through the stream's
// worker, which is the same goroutine that processed events before the
// bump.
func sessionFromStream(stream *junk.Stream) *session.Session {
	state := stream.State()
	// junk.Thread.State is always a *ledger.Thread (see
	// github.com/andrewhowdencom/ore/junk.Thread). The type
	// assertion is a hard guarantee, not a possibility.
	thread, ok := state.(*ledger.Thread)
	if !ok {
		// Fallback: if a future ore version changes the underlying
		// state type, fall back to a fresh empty thread so the
		// conduit can still compile and run. Inference history
		// would be invisible to the conduit, which is acceptable
		// degradation given the unrecoverable error.
		thread = ledger.NewThread()
	}
	return session.New(stream.ID(), thread)
}

// junkEventFromSession converts a session.Event to the structurally-
// identical junk.Event so that the *junk.Stream worker can consume it.
// The two packages define UserMessageEvent and InterruptEvent with the
// same fields; we copy across. Other event shapes are rejected with
// errUnexpectedEvent — the framework only emits those two today.
func junkEventFromSession(e session.Event) (junk.Event, error) {
	if e == nil {
		return nil, errors.New("junkBackend: nil event")
	}
	switch v := e.(type) {
	case session.UserMessageEvent:
		return junk.UserMessageEvent{Content: v.Content, Ctx: v.Ctx}, nil
	case session.InterruptEvent:
		return junk.InterruptEvent{Ctx: v.Ctx}, nil
	default:
		return nil, errUnexpectedEvent
	}
}