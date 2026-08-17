// Package app — Per-turn session persistence via ledger.Repository.
//
// This file holds the SessionPersister, the per-session save hook
// introduced by Task 4 of the kill-junk migration. It subscribes
// to a session's TurnCompleteEvent and appends a journal entry
// per turn via ledger.Repository.SaveTurn / UpdateThreadTip.
//
// Why this exists: pre-migration, junk.Stream.Save() was called
// from the lifecycle pump and wrote a JSON snapshot of the entire
// thread. That path is being retired. The replacement is the
// journal append exposed by ledger.Repository, which:
//
//   - Preserves the turn tree (parent links, control directives)
//     without collapsing it into a snapshot — useful for the
//     compaction path that ControlStop's.
//   - Is single-writer-per-thread (the framework's serial pipeline
//     is the contract); no coordination overhead.
//   - Writes one journal entry per turn (small append, atomic
//     via O_APPEND) — failure of one write doesn't corrupt
//     earlier entries.
//
// Format incompatibility (accepted with documentation):
// The legacy junk.JSONStore wrote <id>.json snapshots; the new
// ledger.FileRepository writes <id>.jsonl journals. The file
// extension differs, so a fresh directory sees no conflict, but
// threads persisted by the old code do not resume after the
// migration. New writes go to the new format; readers find only
// the new format. The plan acknowledges this in its risks
// table; we accept the one-time loss of pre-migration threads
// rather than ship a one-shot conversion tool, because the
// active workshop users are the ones driving the migration and
// can re-run their conversation from the in-memory cache if
// needed.
package app

import (
	"context"
	"log/slog"

	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/session"
)

// SessionPersister is the per-session save hook used by
// runTUIEngine. The implementation in this file subscribes to a
// session's TurnCompleteEvent and appends a journal entry per turn
// via ledger.Repository.SaveTurn / UpdateThreadTip. It replaces the
// post-turn junk.Stream.Save() call that Task 3 of the kill-junk
// migration left in place.
//
// Lifecycle: Attach starts a goroutine that ranges over the
// subscription. The goroutine exits when the session is closed
// (which propagates through the session's step into the
// subscription channel). Callers do not need to invoke Close
// explicitly; Close is provided for symmetry with other
// persistence interfaces and is a no-op today.
type SessionPersister interface {
	Attach(sess *session.Session) error
	Close() error
}

// subscriptionPersister is the concrete implementation of
// SessionPersister. It is returned by NewSessionPersister and
// consumed by runTUIEngine via the SessionPersister interface.
type subscriptionPersister struct {
	repo ledger.Repository
	log  *slog.Logger
}

// NewSessionPersister constructs a SessionPersister bound to the
// given ledger.Repository. A nil log falls back to slog.Default().
func NewSessionPersister(repo ledger.Repository, log *slog.Logger) SessionPersister {
	if log == nil {
		log = slog.Default()
	}
	return &subscriptionPersister{repo: repo, log: log}
}

// Attach starts the per-turn save loop. The loop subscribes to the
// session's "turn_complete" event channel and ranges over it,
// persisting each event. The goroutine exits naturally when the
// session is closed (sess.Close propagates through the step's
// FanOut into the subscription channel).
//
// Failures are logged via slog.Warn and do not abort the loop;
// persistence failures are not fatal for an interactive TUI
// session — the user can retry by typing.
func (p *subscriptionPersister) Attach(sess *session.Session) error {
	if sess == nil {
		return nil
	}
	go func() {
		for event := range sess.Subscribe("turn_complete") {
			te, ok := event.(loop.TurnCompleteEvent)
			if !ok {
				continue
			}
			if err := p.persist(sess, te); err != nil {
				p.log.Warn("persist turn failed", "err", err, "turn_id", te.Turn.ID)
			}
		}
	}()
	return nil
}

// persist appends one journal entry per turn. It calls SaveTurn
// for the assistant turn and UpdateThreadTip for the current tip;
// both are journal appends via the Repository. The ctx is a fresh
// background context because the subscription's lifetime is not
// tied to any caller context.
func (p *subscriptionPersister) persist(sess *session.Session, ev loop.TurnCompleteEvent) error {
	ctx := context.Background()
	threadID := sess.ID()
	if err := p.repo.SaveTurn(ctx, threadID, &ev.Turn); err != nil {
		return err
	}
	if tip := sess.Thread().CurrentTip; tip != "" {
		if err := p.repo.UpdateThreadTip(ctx, threadID, tip); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op. The subscription channel is closed when the
// session is closed, which lets the goroutine started by Attach
// drain and exit. The method is provided for symmetry with other
// persistence interfaces.
func (p *subscriptionPersister) Close() error {
	return nil
}