// Package compaction trims conversation state to fit within a provider's
// context-window budget. It exposes a configurable Strategy that decides
// which messages to keep or drop, and a loop.Transform implementation that
// applies the strategy before every inference turn.
package compaction

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/state"
)

// Message is a flat, token-estimated representation of a single turn
// used for budgeting. It carries enough metadata for strategies to make
// eviction decisions.
type Message struct {
	Role      state.Role
	Artifacts []artifact.Artifact
	Tokens    int
}

// MessagesFromTurns flattens state.Turn into []Message and estimates
// tokens for each turn.
func MessagesFromTurns(turns []state.Turn, estimate EstimateFunc) []Message {
	msgs := make([]Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, Message{
			Role:      t.Role,
			Artifacts: t.Artifacts,
			Tokens:    estimate(t),
		})
	}
	return msgs
}

// EstimateFunc approximates the token cost of a single turn.
type EstimateFunc func(turn state.Turn) int

// DefaultEstimate counts runes and divides by four. This is a coarse
// approximation (~0.25 tokens/char for English text) that is good enough
// for budgeting without pulling in a full tokenizer.
func DefaultEstimate(turn state.Turn) int {
	total := 0
	for _, art := range turn.Artifacts {
		switch a := art.(type) {
		case interface{ Content() string }:
			total += utf8.RuneCountInString(a.Content())
		case interface{ GetContent() string }:
			total += utf8.RuneCountInString(a.GetContent())
		default:
			// Fall back to Kind() length as a minimal proxy.
			total += len(a.Kind())
		}
	}
	// Add a per-turn overhead to account for role delimiters, etc.
	const overhead = 4
	tokens := (total / 4) + overhead
	if tokens < 1 {
		return 1
	}
	return tokens
}

// Strategy decides how to shrink a message list to fit a token budget.
type Strategy interface {
	// Compact receives the full message history and a token budget (max
	// tokens allowed). It returns a reduced slice whose total token count
	// is <= budget. The returned slice should be ordered newest-last.
	Compact(msgs []Message, budget int) []Message
}

// Budget is a token ceiling derived from a provider's context limit and a
// safety margin.
type Budget struct {
	// Max is the provider context-window size in tokens.
	Max int
	// Reserve is a fixed number of tokens always left free for the model's
	// response and any per-turn overhead. The effective budget is Max-Reserve.
	Reserve int
}

// Effective returns Max - Reserve, floored at zero.
func (b Budget) Effective() int {
	if v := b.Max - b.Reserve; v > 0 {
		return v
	}
	return 0
}

// Transform applies a Strategy to state before each provider call. It
// implements loop.Transform.
type Transform struct {
	Strategy Strategy
	Budget   Budget
	Estimate EstimateFunc
}

// Compile-time check.
var _ loop.Transform = (*Transform)(nil)

// Transform compacts the state's turn history so the total estimated
// tokens fit within Budget.Effective(). It returns a state.View over the
// reduced turns.
func (t *Transform) Transform(ctx context.Context, st state.State) (state.State, error) {
	turns := st.Turns()
	if len(turns) == 0 {
		return st, nil
	}

	budget := t.Budget.Effective()
	if budget <= 0 {
		return nil, fmt.Errorf("compaction budget exhausted (max=%d reserve=%d)", t.Budget.Max, t.Budget.Reserve)
	}

	msgs := MessagesFromTurns(turns, t.Estimate)
	kept := t.Strategy.Compact(msgs, budget)

	// If nothing was compacted, return the original state unmodified.
	if len(kept) == len(turns) {
		return st, nil
	}

	// Re-build the reduced turn slice from kept messages.
	reduced := make([]state.Turn, len(kept))
	for i, m := range kept {
		reduced[i] = state.Turn{Role: m.Role, Artifacts: m.Artifacts}
	}

	return stateView{turns: reduced}, nil
}

// stateView is a lightweight read-only wrapper over a fixed turn slice.
// It implements state.State.
type stateView struct {
	turns []state.Turn
}

func (v stateView) Turns() []state.Turn { return v.turns }
func (v stateView) Append(state.Role, ...artifact.Artifact) {
	// Append is intentionally a no-op on a view. The transform must not
	// mutate the underlying persistent state.
}
