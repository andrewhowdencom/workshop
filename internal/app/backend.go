// Package app — Backend adapter for the ore v1.x session-based conduit API.
//
// Background
//
// The HTTP conduit (x/conduit/http) follows the session-based contract
// documented in x/conduit/doc.go: it consumes a Backend interface that
// speaks in terms of session.Session, session.Event, and the
// ledger-backed durable store. The application is responsible for
// wiring that surface together — registry for active session lookup,
// repository for hydration/persistence, engine for event submission.
//
// The sessionBackend below is the workshop's implementation of
// httpc.Backend. It is intentionally narrow: each method maps to a
// single, well-defined contract (CreateSession/GetSession/Submit/
// ListThreads/DeleteSession) and delegates to the session/registry/
// repository/engine primitives rather than re-implementing them.
//
// Design notes
//
//  1. CreateSession with a non-empty threadID hydrates from the
//     repository; with an empty threadID it constructs a fresh
//     ephemeral thread. Both paths register the session with the
//     registry so it is reachable by GetSession and by the engine.
//  2. Submit enqueues onto the engine's per-session mailbox.
//     Inference is driven by the engine's factory (built
//     upstream of the backend in setupSession).
//  3. ListThreads enumerates durable thread IDs via the repository
//     and hydrates each one to extract a last-activity timestamp;
//     malformed threads are skipped (consistent with the previous
//     junkBackend.ListThreads tolerance for unreadable files).
//  4. DeleteSession removes the session from the active registry.
//     The thread itself is not deleted from durable storage;
//     re-attachment via CreateSession(threadID) recovers it.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/andrewhowdencom/ore/engine"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/session"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
)

// sessionBackend adapts the session/registry/repository/engine
// quartet onto the httpc.Backend interface required by ore's
// session-shaped HTTP conduit.
type sessionBackend struct {
	registry session.Registry
	repo     ledger.Repository
	eng      *engine.Engine
}

// newSessionBackend constructs a Backend adapter around the given
// primitives. All four are required.
func newSessionBackend(
	reg session.Registry,
	repo ledger.Repository,
	eng *engine.Engine,
) *sessionBackend {
	return &sessionBackend{registry: reg, repo: repo, eng: eng}
}

// Compile-time assertion that *sessionBackend satisfies httpc.Backend.
var _ httpc.Backend = (*sessionBackend)(nil)

// errUnexpectedEvent is reserved for future event-type drift. Today
// session.Event and engine.Submit consume the same event types
// directly, so the Submit path does not need translation.
var errUnexpectedEvent = errors.New("sessionBackend: unsupported event type")

// CreateSession creates a fresh *session.Session when threadID is
// empty, or attaches to an existing thread (hydrated from the
// repository) when threadID is non-empty. The returned session is
// registered with the registry.
func (b *sessionBackend) CreateSession(ctx context.Context, threadID string) (*session.Session, error) {
	if threadID == "" {
		// Fresh ephemeral session: new thread, register, return.
		id := generateThreadID()
		thread := ledger.NewThread()
		sess := session.New(id, thread)
		if err := b.registry.Register(sess); err != nil {
			return nil, fmt.Errorf("register session %s: %w", id, err)
		}
		return sess, nil
	}

	// Attach: hydrate the thread from the repository, then
	// register the resulting session.
	turns, tip, err := b.repo.HydrateThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("hydrate thread %s: %w", threadID, err)
	}
	thread := ledger.NewThread()
	for _, t := range turns {
		thread.SaveTurn(t)
	}
	thread.SetCurrentTip(tip)

	sess := session.New(threadID, thread)
	if err := b.registry.Register(sess); err != nil {
		return nil, fmt.Errorf("register session %s: %w", threadID, err)
	}
	return sess, nil
}

// GetSession looks up an active session by ID via the registry.
// Returns an error wrapping session.ErrSessionNotFound when no
// session is registered under the given ID.
func (b *sessionBackend) GetSession(ctx context.Context, id string) (*session.Session, error) {
	return b.registry.Get(id)
}

// Submit enqueues an event for the given session via the engine's
// per-session mailbox. The session must be registered (reachable
// via GetSession).
func (b *sessionBackend) Submit(ctx context.Context, id string, event session.Event) error {
	return b.eng.Submit(ctx, id, event)
}

// ListThreads enumerates every durable thread via the repository.
// For each thread ID it derives a last-activity timestamp from the
// most-recently-appended turn. Malformed threads (hydration error)
// are skipped — the listing tolerates individual failures the
// way the prior junkBackend tolerated unreadable JSON files.
func (b *sessionBackend) ListThreads(ctx context.Context) ([]httpc.ThreadSummary, error) {
	ids, err := b.repo.ListThreadIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list thread ids: %w", err)
	}
	out := make([]httpc.ThreadSummary, 0, len(ids))
	for _, id := range ids {
		turns, _, err := b.repo.HydrateThread(ctx, id)
		if err != nil {
			continue
		}
		var lastAt time.Time
		for _, t := range turns {
			if t.Timestamp.After(lastAt) {
				lastAt = t.Timestamp
			}
		}
		out = append(out, httpc.ThreadSummary{
			ID:     id,
			LastAt: lastAt,
		})
	}
	return out, nil
}

// DeleteSession removes the session from the active registry.
// The durable thread is NOT deleted; it can be re-attached later
// via CreateSession with a non-empty threadID.
func (b *sessionBackend) DeleteSession(ctx context.Context, id string) error {
	_, err := b.registry.Remove(id)
	return err
}

// generateThreadID returns a new thread identifier. UUIDv4 matches
// the format expected by downstream tooling; it is also already an
// indirect dependency of the module (via ore) so no go.mod change
// is required to use it directly.
func generateThreadID() string {
	return uuid.New().String()
}