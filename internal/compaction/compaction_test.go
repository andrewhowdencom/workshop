package compaction

import (
	"testing"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/state"
)

func textArtifact(content string) []artifact.Artifact {
	return []artifact.Artifact{artifact.Text{Content: content}}
}

func TestFIFO(t *testing.T) {
	cases := []struct {
		name     string
		msgs     []Message
		budget   int
		wantLen  int
		wantHead state.Role // expected role of first kept message
	}{
		{
			name: "under budget — no eviction",
			msgs: []Message{
				{Role: state.RoleUser, Tokens: 10},
				{Role: state.RoleAssistant, Tokens: 10},
			},
			budget:   30,
			wantLen:  2,
			wantHead: state.RoleUser,
		},
		{
			name: "exactly at budget — no eviction",
			msgs: []Message{
				{Role: state.RoleUser, Tokens: 10},
				{Role: state.RoleAssistant, Tokens: 20},
			},
			budget:   30,
			wantLen:  2,
			wantHead: state.RoleUser,
		},
		{
			name: "over budget — drops oldest",
			msgs: []Message{
				{Role: state.RoleUser, Tokens: 10},
				{Role: state.RoleAssistant, Tokens: 10},
				{Role: state.RoleUser, Tokens: 10},
				{Role: state.RoleAssistant, Tokens: 10},
			},
			budget:   25,
			wantLen:  2,
			wantHead: state.RoleUser, // third message (role=user)
		},
		{
			name: "single message over budget — keeps it",
			msgs: []Message{
				{Role: state.RoleUser, Tokens: 100},
			},
			budget:   10,
			wantLen:  1,
			wantHead: state.RoleUser,
		},
		{
			name: "drops multiple oldest to fit",
			msgs: []Message{
				{Role: state.RoleSystem, Tokens: 10},
				{Role: state.RoleUser, Tokens: 10},
				{Role: state.RoleAssistant, Tokens: 10},
				{Role: state.RoleUser, Tokens: 5},
			},
			budget:   20,
			wantLen:  2,
			wantHead: state.RoleAssistant, // third message
		},
	}

	var f FIFO
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := f.Compact(c.msgs, c.budget)
			if len(got) != c.wantLen {
				t.Fatalf("len=%d, want %d", len(got), c.wantLen)
			}
			if len(got) > 0 && got[0].Role != c.wantHead {
				t.Fatalf("first role=%q, want %q", got[0].Role, c.wantHead)
			}
		})
	}
}

func TestTransform(t *testing.T) {
	tr := &Transform{
		Strategy: FIFO{},
		Budget:   Budget{Max: 100, Reserve: 20}, // effective=80
		Estimate: func(turn state.Turn) int {
			total := 0
			for _, a := range turn.Artifacts {
				if ta, ok := a.(artifact.Text); ok {
					total += len(ta.Content)
				}
			}
			return total
		},
	}

	// Build a simple in-memory state for testing.
	ms := &mockState{
		turns: []state.Turn{
			{Role: state.RoleSystem, Artifacts: textArtifact("system prompt")},
			{Role: state.RoleUser, Artifacts: textArtifact("hello")},
			{Role: state.RoleAssistant, Artifacts: textArtifact("world")},
		},
	}

	got, err := tr.Transform(t.Context(), ms)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}

	// All three turns fit within 80 tokens, so nothing is evicted.
	if len(got.Turns()) != 3 {
		t.Fatalf("len=%d, want 3", len(got.Turns()))
	}
}

type mockState struct {
	turns []state.Turn
}

func (m *mockState) Turns() []state.Turn { return append([]state.Turn(nil), m.turns...) }
func (m *mockState) Append(state.Role, ...artifact.Artifact) {}
