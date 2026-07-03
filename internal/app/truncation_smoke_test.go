package app

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/loop"
	"github.com/andrewhowdencom/ore/tool"
	xtool "github.com/andrewhowdencom/ore/x/tool"
)

// captureEmitter records every OutputEvent passed to Emit so the test
// can inspect the final ToolResult without a real loop.Step.
type captureEmitter struct {
	mu     sync.Mutex
	events []loop.OutputEvent
}

func (c *captureEmitter) Emit(_ context.Context, e loop.OutputEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureEmitter) lastToolResult(t *testing.T) (string, *artifact.Truncation) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		tc, ok := c.events[i].(loop.TurnCompleteEvent)
		if !ok {
			continue
		}
		for _, a := range tc.Turn.Artifacts {
			tr, ok := a.(artifact.ToolResult)
			if !ok {
				continue
			}
			return tr.Content, tr.Truncation
		}
	}
	t.Fatal("no ToolResult emitted")
	return "", nil
}

// TestWorkshop_FrameworkTruncation_BoundsLargeJSON confirms the
// "framework defaults to safe" contract: a tool registered the
// same way workshop registers ad-hoc raw tools (mustRegisterRaw,
// no explicit Format) that returns a multi-MB JSON-serializable
// struct is bounded by xtool.NewHandler to the 50 KB / 2000 line
// defaults before being emitted.
func TestWorkshop_FrameworkTruncation_BoundsLargeJSON(t *testing.T) {
	registry := tool.NewRegistry()

	type bigOut struct {
		Line string `json:"line"`
	}
	big := make([]bigOut, 0, 60000)
	line := strings.Repeat("x", 100)
	for i := 0; i < 60000; i++ {
		big = append(big, bigOut{Line: line})
	}

	fn := func(_ context.Context, _ tool.Sandbox, _ map[string]any) (any, error) {
		return big, nil
	}
	mustRegisterRaw(registry, "huge_echo", "returns a huge JSON list", map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}, fn)

	handler := xtool.NewHandler(registry)
	em := &captureEmitter{}

	err := handler.Handle(context.Background(), artifact.ToolCall{
		ID:        "call-1",
		Name:      "huge_echo",
		Arguments: "{}",
	}, em)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	content, trunc := em.lastToolResult(t)
	t.Logf("emitted content length: %d bytes", len(content))
	if trunc == nil {
		t.Fatalf("expected Truncation to be set, got nil. content head: %q", content[:min(200, len(content))])
	}
	t.Logf("truncation: orig=%d bytes / %d lines, shown=%d bytes / %d lines, style=%q",
		trunc.OriginalBytes, trunc.OriginalLines, trunc.ShownBytes, trunc.ShownLines, trunc.Style)

	if trunc.OriginalBytes < 5*1024*1024 {
		t.Fatalf("OriginalBytes should reflect the multi-MB input; got %d", trunc.OriginalBytes)
	}
	if trunc.ShownBytes > 50_000 {
		t.Fatalf("framework default MaxBytes should bound shown to 50 KB; got %d", trunc.ShownBytes)
	}
	if trunc.ShownLines > 2000 {
		t.Fatalf("framework default MaxLines should bound shown to 2000; got %d", trunc.ShownLines)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
