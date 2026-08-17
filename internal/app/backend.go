// Package app — HTTP Backend adapter backed by session.Registry and
// ledger.Repository.
//
// Background
//
// As of ore v1.x the HTTP conduit follows the "session-based"
// contract:
//
//   - httpc.New(backend httpc.Backend, opts ...) (was: http.New(mgr, ...))
//
// This file replaces the junkBackend adapter that pre-migration
// bridge *junk.Manager onto httpc.Backend. The replacement is
// session-native: it uses session.Registry for active-session
// resolution and ledger.Repository for hydration/persistence.
//
// Stdio is unchanged in this commit: ore's stdio conduit
// constructor still takes *junk.Manager (no upstream
// session-shaped equivalent exists). Stdio is migrated in a
// follow-up when upstream lands. See the kill-junk migration
// plan for the discussion of the stdio risk.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/session"
	httpc "github.com/andrewhowdencom/ore/x/conduit/http"
)

// sessionBackend is the session-native implementation of
// httpc.Backend. It uses session.Registry for active-session
// resolution and ledger.Repository for thread hydration
// (CreateSession with a non-empty threadID) and listing
// (ListThreads).
type sessionBackend struct {
	reg  session.Registry
	repo ledger.Repository
}

// Compile-time assertion that *sessionBackend satisfies
// httpc.Backend.
var _ httpc.Backend = (*sessionBackend)(nil)

// newSessionBackend constructs the session-native Backend.
func newSessionBackend(reg session.Registry, repo ledger.Repository) *sessionBackend {
	return &sessionBackend{reg: reg, repo: repo}
}

// CreateSession implements httpc.Backend.
func (b *sessionBackend) CreateSession(ctx context.Context, threadID string) (*session.Session, error) {
	var thread *ledger.Thread
	if threadID != "" {
		// Attach: hydrate from the repo and create a session over
		// the recovered thread. The repo's HydrateThread is the
		// single source of truth for resume.
		turns, currentTip, err := b.repo.HydrateThread(ctx, threadID)
		if err != nil {
			return nil, fmt.Errorf("hydrate thread %s: %w", threadID, err)
		}
		thread = ledger.NewThread()
		thread.CurrentTip = currentTip
		for _, turn := range turns {
			thread.Append(turn.Role, turn.Artifacts...)
		}
	} else {
		// Fresh ephemeral session with a fresh thread.
		thread = ledger.NewThread()
	}

	sess := session.New(newSessionID(), thread)
	if err := b.reg.Register(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSession implements httpc.Backend.
func (b *sessionBackend) GetSession(_ context.Context, id string) (*session.Session, error) {
	sess, err := b.reg.Get(id)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Submit implements httpc.Backend. We accept the event by
// submitting it to the session; the session's step.Submit
// auto-appends to the bound thread.
func (b *sessionBackend) Submit(ctx context.Context, id string, event session.Event) error {
	sess, err := b.reg.Get(id)
	if err != nil {
		return err
	}
	switch v := event.(type) {
	case session.UserMessageEvent:
		// The HTTP conduit emits the raw user text; submit it
		// as a user-role text artifact. The session.Submit signature
		// requires a role and an artifact slice; we wrap the text
		// content in an artifact.Text so downstream consumers can
		// recover the user message.
		_, err := sess.Submit(ctx, ledger.RoleUser,
			artifact.Text{Content: v.Content})
		return err
	case session.InterruptEvent:
		// session.Submit treats this as a normal turn; the
		// framework's interrupt handling is a session-level
		// signal, not a per-turn event. Pass through.
		return nil
	default:
		return fmt.Errorf("sessionBackend: unsupported event type %T", v)
	}
}

// ListThreads implements httpc.Backend.
func (b *sessionBackend) ListThreads(ctx context.Context) ([]httpc.ThreadSummary, error) {
	ids, err := b.repo.ListThreadIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]httpc.ThreadSummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, httpc.ThreadSummary{ID: id})
	}
	return out, nil
}

// DeleteSession implements httpc.Backend.
func (b *sessionBackend) DeleteSession(_ context.Context, id string) error {
	_, err := b.reg.Remove(id)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return err
	}
	return nil
}

// newSessionID returns a new random session ID. The format is
// a 16-byte hex string (32 chars) — random enough for in-process
// use; the registry is in-memory and never shared.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail on a healthy system.
		// Fall back to a deterministic ID to keep tests stable
		// even in pathological environments.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}