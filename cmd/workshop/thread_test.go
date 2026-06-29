package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/state"
	"github.com/spf13/viper"
)

// TestThreadList_EmptyStoreDir_FallsBackToDefault is intentionally
// NOT a test. An earlier version asserted the command runs without
// error when store.dir is empty, but that depended on the default
// XDG data directory being clean. On machines with prior workshop
// sessions, that directory can contain thread files that the JSON
// store cannot parse, and the resulting panic (in junk's
// unmarshalTurns, not in workshop code) propagates out of List() and
// fails the test for an environmental reason.
//
// The fallback itself is a one-line `if storeDir == ""` in
// runThreadList; it is exercised by every other test that calls
// RunE without setting store.dir, and it does not justify the
// fragility of reading the real XDG path. If we ever need explicit
// coverage, the right shape is to set XDG_DATA_HOME to a temp dir
// for the duration of the test.

func TestThreadList_WithStore(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr1, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thr1.Metadata["workshop.role"] = "developer"
	if err := store.Save(thr1); err != nil {
		t.Fatalf("save thread 1: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	thr2, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thr2.Metadata["workshop.role"] = "reviewer"
	if err := store.Save(thr2); err != nil {
		t.Fatalf("save thread 2: %v", err)
	}

	oldStoreDir := viper.GetString("store.dir")
	viper.Set("store.dir", tmpDir)
	t.Cleanup(func() { viper.Set("store.dir", oldStoreDir) })

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	err = threadListCmd.RunE(threadListCmd, []string{})

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("threadListCmd.RunE: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, thr1.ID) {
		t.Errorf("output missing thread 1 ID: %s", output)
	}
	if !strings.Contains(output, thr2.ID) {
		t.Errorf("output missing thread 2 ID: %s", output)
	}
	if !strings.Contains(output, "developer") {
		t.Errorf("output missing developer role: %s", output)
	}
	if !strings.Contains(output, "reviewer") {
		t.Errorf("output missing reviewer role: %s", output)
	}

	// Verify sort order: thread2 (more recent) should appear before thread1.
	idx1 := strings.Index(output, thr1.ID)
	idx2 := strings.Index(output, thr2.ID)
	if idx1 == -1 || idx2 == -1 {
		t.Fatalf("could not find thread IDs in output")
	}
	if idx2 > idx1 {
		t.Errorf("sort order wrong: thread2 should appear before thread1; idx1=%d, idx2=%d", idx1, idx2)
	}
}

// TestThreadList_Pagination_DefaultSort covers the default sort
// order: most recently updated first, with id ascending as the
// deterministic tiebreaker. Three threads spaced in time are saved
// in known order; the test verifies the rendered output lists them
// from most-recent to least-recent.
func TestThreadList_Pagination_DefaultSort(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr1, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thr1.Metadata["workshop.role"] = "a"
	if err := store.Save(thr1); err != nil {
		t.Fatalf("save thread 1: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	thr2, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thr2.Metadata["workshop.role"] = "b"
	if err := store.Save(thr2); err != nil {
		t.Fatalf("save thread 2: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	thr3, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 3: %v", err)
	}
	thr3.Metadata["workshop.role"] = "c"
	if err := store.Save(thr3); err != nil {
		t.Fatalf("save thread 3: %v", err)
	}

	var buf bytes.Buffer
	if err := runThreadListWithStore(20, "", false, store, &buf); err != nil {
		t.Fatalf("runThreadListWithStore: %v", err)
	}

	output := buf.String()
	idx1 := strings.Index(output, thr1.ID)
	idx2 := strings.Index(output, thr2.ID)
	idx3 := strings.Index(output, thr3.ID)
	if idx1 == -1 || idx2 == -1 || idx3 == -1 {
		t.Fatalf("could not find all thread IDs in output:\n%s", output)
	}
	// Most recent first.
	if !(idx3 < idx2 && idx2 < idx1) {
		t.Errorf("sort order wrong: expected thr3<thr2<thr1; got idx1=%d, idx2=%d, idx3=%d",
			idx1, idx2, idx3)
	}
	if strings.Contains(output, "-- next:") {
		t.Errorf("no hint expected when all 3 threads fit in a single page:\n%s", output)
	}
}

// TestThreadList_Pagination_LimitHonored seeds more threads than
// fit in one page and asserts the limit is respected, with the
// remaining threads reported via the --next hint line.
func TestThreadList_Pagination_LimitHonored(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		thr, err := store.Create()
		if err != nil {
			t.Fatalf("create thread %d: %v", i, err)
		}
		thr.Metadata["workshop.role"] = "r"
		if err := store.Save(thr); err != nil {
			t.Fatalf("save thread %d: %v", i, err)
		}
		ids = append(ids, thr.ID)
		time.Sleep(2 * time.Millisecond)
	}

	var buf bytes.Buffer
	if err := runThreadListWithStore(2, "", false, store, &buf); err != nil {
		t.Fatalf("runThreadListWithStore(2): %v", err)
	}

	output := buf.String()
	// The two most recent threads should be present.
	if !strings.Contains(output, ids[4]) {
		t.Errorf("output missing most recent ID %s:\n%s", ids[4], output)
	}
	if !strings.Contains(output, ids[3]) {
		t.Errorf("output missing second-most recent ID %s:\n%s", ids[3], output)
	}
	// Older threads should be on the next page, not on this one.
	for _, id := range ids[:3] {
		if strings.Contains(output, id) {
			t.Errorf("output unexpectedly contains older ID %s:\n%s", id, output)
		}
	}
	// Hint line with cursor.
	if !strings.Contains(output, "-- next: --cursor ") {
		t.Errorf("expected hint line in output:\n%s", output)
	}
}

// TestThreadList_Pagination_AllWalksAllPages seeds several threads
// and asserts --all renders every thread exactly once, with no hint
// line.
func TestThreadList_Pagination_AllWalksAllPages(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	want := make(map[string]bool)
	for i := 0; i < 5; i++ {
		thr, err := store.Create()
		if err != nil {
			t.Fatalf("create thread %d: %v", i, err)
		}
		if err := store.Save(thr); err != nil {
			t.Fatalf("save thread %d: %v", i, err)
		}
		want[thr.ID] = true
		time.Sleep(2 * time.Millisecond)
	}

	var buf bytes.Buffer
	if err := runThreadListWithStore(2, "", true, store, &buf); err != nil {
		t.Fatalf("runThreadListWithStore(all=true): %v", err)
	}

	output := buf.String()
	for id := range want {
		if !strings.Contains(output, id) {
			t.Errorf("--all output missing %s:\n%s", id, output)
		}
	}
	if strings.Contains(output, "-- next:") {
		t.Errorf("--all should suppress the hint line:\n%s", output)
	}
}

// TestThreadList_Pagination_CursorRoundTrip confirms the cursor
// returned in the hint line, when fed back into --cursor, continues
// the listing from the next page.
func TestThreadList_Pagination_CursorRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		thr, err := store.Create()
		if err != nil {
			t.Fatalf("create thread %d: %v", i, err)
		}
		if err := store.Save(thr); err != nil {
			t.Fatalf("save thread %d: %v", i, err)
		}
		ids = append(ids, thr.ID)
		time.Sleep(2 * time.Millisecond)
	}

	// Page 1.
	var page1 bytes.Buffer
	if err := runThreadListWithStore(2, "", false, store, &page1); err != nil {
		t.Fatalf("page 1: %v", err)
	}
	out1 := page1.String()
	if !strings.Contains(out1, ids[3]) || !strings.Contains(out1, ids[2]) {
		t.Errorf("page 1 missing expected IDs:\n%s", out1)
	}
	// Extract the cursor from the hint line.
	const prefix = "-- next: --cursor "
	idx := strings.Index(out1, prefix)
	if idx == -1 {
		t.Fatalf("page 1 missing cursor hint:\n%s", out1)
	}
	cursor := strings.TrimSpace(out1[idx+len(prefix):])
	if cursor == "" {
		t.Fatal("page 1 cursor is empty")
	}

	// Page 2.
	var page2 bytes.Buffer
	if err := runThreadListWithStore(2, cursor, false, store, &page2); err != nil {
		t.Fatalf("page 2: %v", err)
	}
	out2 := page2.String()
	if !strings.Contains(out2, ids[1]) || !strings.Contains(out2, ids[0]) {
		t.Errorf("page 2 missing expected IDs:\n%s", out2)
	}
	// Last page: no hint line.
	if strings.Contains(out2, "-- next:") {
		t.Errorf("last page should not emit a hint:\n%s", out2)
	}
}

// TestThreadList_Pagination_InvalidCursor confirms that an
// unparseable cursor produces an error mentioning "cursor".
func TestThreadList_Pagination_InvalidCursor(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadListWithStore(20, "!!!not-base64!!!", false, store, &buf)
	if err == nil {
		t.Fatal("expected error for invalid cursor, got nil")
	}
	if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("error should mention 'cursor': %v", err)
	}
}

// TestThreadList_Pagination_LimitClamping seeds two threads and
// confirms that limit values outside [1, MaxPageSize] are silently
// clamped: limit=0 and limit=-5 yield 1 thread, limit=99999 yields
// both threads (and no hint line, since both fit on one page).
func TestThreadList_Pagination_LimitClamping(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr1, err := store.Create()
	if err != nil {
		t.Fatalf("create thr1: %v", err)
	}
	thr1.Metadata["workshop.role"] = "a"
	if err := store.Save(thr1); err != nil {
		t.Fatalf("save thr1: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	thr2, err := store.Create()
	if err != nil {
		t.Fatalf("create thr2: %v", err)
	}
	thr2.Metadata["workshop.role"] = "b"
	if err := store.Save(thr2); err != nil {
		t.Fatalf("save thr2: %v", err)
	}

	tests := []struct {
		name    string
		limit   int
		wantIDs int
		hint    bool
	}{
		{"zero clamps to one", 0, 1, true},
		{"negative clamps to one", -5, 1, true},
		{"oversize clamps to MaxPageSize", 99999, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := runThreadListWithStore(tt.limit, "", false, store, &buf); err != nil {
				t.Fatalf("runThreadListWithStore(%d): %v", tt.limit, err)
			}
			out := buf.String()
			count := 0
			if strings.Contains(out, thr1.ID) {
				count++
			}
			if strings.Contains(out, thr2.ID) {
				count++
			}
			if count != tt.wantIDs {
				t.Errorf("expected %d IDs in output, got %d:\n%s", tt.wantIDs, count, out)
			}
			hintPresent := strings.Contains(out, "-- next:")
			if hintPresent != tt.hint {
				t.Errorf("hint presence: got %v, want %v:\n%s", hintPresent, tt.hint, out)
			}
		})
	}
}

// TestThreadList_RemovedDaysFlag confirms that --days is no longer
// accepted on `thread list`. This is the user-visible half of the
// viper binding fix: the buggy silent ignore is replaced with a
// loud cobra "unknown flag" error.
func TestThreadList_RemovedDaysFlag(t *testing.T) {
	tmpDir := t.TempDir()

	oldStoreDir := viper.GetString("store.dir")
	viper.Set("store.dir", tmpDir)
	t.Cleanup(func() { viper.Set("store.dir", oldStoreDir) })

	// Reset the command's flags so prior test runs do not pollute
	// the parse. cobra stores parsed state on the cmd; flags persist
	// defaults across calls but parsed values are scoped to a
	// single ParseFlags invocation.
	err := threadListCmd.ParseFlags([]string{"--days", "1"})
	if err == nil {
		t.Fatal("expected cobra to reject --days on thread list, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") &&
		!strings.Contains(err.Error(), "--days") {
		t.Errorf("error should mention the rejected --days flag: %v", err)
	}
}

// TestThreadAnalytics_DaysFlagRegression is the regression test for
// the viper binding collision that previously caused --days on
// `thread list` to be silently ignored. The fix reads --days from
// cmd.Flags() instead of viper, so a stale value in viper cannot
// override the user's CLI argument. To prove this, the test seeds
// viper with a deliberately wrong lookback and asserts that
// `thread analytics --days 30` still honours 30.
func TestThreadAnalytics_DaysFlagRegression(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// Recent thread with a 5-byte text artifact.
	recent, err := store.Create()
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.State.Append(state.RoleUser, artifact.Text{Content: "fresh"})
	if err := store.Save(recent); err != nil {
		t.Fatalf("save recent thread: %v", err)
	}

	// Old thread (60 days ago) with a 5-byte text artifact. The shape
	// must match junk/serialize.go (a {kind, data} envelope
	// around the artifact body), otherwise JSONStore silently skips
	// the file.
	oldID := "00000000000000000000000000000001"
	oldTime := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	oldJSON := fmt.Sprintf(
		`{"id":"%s","created_at":"%s","updated_at":"%s","turns":[{"role":"user","artifacts":[{"kind":"text","data":{"kind":"text","content":"stale"}}],"timestamp":"%s"}]}`,
		oldID, oldTime, oldTime, oldTime,
	)
	oldPath := filepath.Join(tmpDir, oldID+".json")
	if err := os.WriteFile(oldPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old thread file: %v", err)
	}

	// Simulate the buggy viper state: if viper.GetInt("days") were
	// consulted, this 90 would win. With the fix, --days 30 from the
	// CLI overrides it.
	oldViper := viper.Get("days")
	viper.Set("days", 90)
	t.Cleanup(func() { viper.Set("days", oldViper) })

	oldStoreDir := viper.GetString("store.dir")
	viper.Set("store.dir", tmpDir)
	t.Cleanup(func() { viper.Set("store.dir", oldStoreDir) })

	// Capture stdout from RunE.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	runErr := threadAnalyticsCmd.RunE(threadAnalyticsCmd, []string{"--days", "30"})

	w.Close()
	os.Stdout = oldStdout

	if runErr != nil {
		t.Fatalf("threadAnalyticsCmd.RunE: %v", runErr)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	output := buf.String()

	// With --days 30 the old thread (60 days ago) is excluded, so
	// only the recent thread's 5 bytes appear. If the bug were
	// present (viper.GetInt consulted), --days 90 would include both,
	// yielding 10 bytes for the text row.
	if !strings.Contains(output, "5") {
		t.Errorf("output should include the recent thread's 5-byte text contribution:\n%s", output)
	}
	if strings.Contains(output, "10") {
		t.Errorf("output should NOT include 10 bytes (would mean --days 30 was ignored in favour of viper's 90):\n%s", output)
	}
}

func TestThreadExport_Success(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	formats := []string{"text", "json", "html"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := runThreadExportWithStore(store, thr.ID, format, &buf); err != nil {
				t.Fatalf("runThreadExportWithStore(%s): %v", format, err)
			}

			output := buf.String()
			if output == "" {
				t.Errorf("expected non-empty output for format %s", format)
			}

			switch format {
			case "text":
				if !strings.Contains(output, thr.ID) {
					t.Errorf("text output missing thread ID: %s", output)
				}
			case "json":
				if !strings.Contains(output, `"id"`) {
					t.Errorf("json output missing id field: %s", output)
				}
			case "html":
				if !strings.Contains(output, "<!DOCTYPE html>") {
					t.Errorf("html output missing DOCTYPE: %s", output)
				}
			}
		})
	}
}

func TestThreadExport_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadExportWithStore(store, "nonexistent-id", "text", &buf)
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("error message missing 'thread not found': %v", err)
	}
}

func TestThreadExport_FileOutput(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "output.txt")

	oldStoreDir := viper.GetString("store.dir")
	oldOutput := viper.GetString("output")
	viper.Set("store.dir", tmpDir)
	viper.Set("output", outputFile)
	t.Cleanup(func() {
		viper.Set("store.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	if err := threadExportCmd.RunE(threadExportCmd, []string{thr.ID}); err != nil {
		t.Fatalf("threadExportCmd.RunE: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}

	if !strings.Contains(string(content), thr.ID) {
		t.Errorf("file output missing thread ID: %s", string(content))
	}
}

func TestThreadExport_UnsupportedFormat(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadExportWithStore(store, thr.ID, "xml", &buf)
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Errorf("error message missing 'unsupported format': %v", err)
	}
}

func TestThreadExport_ArgValidation(t *testing.T) {
	t.Run("zero args", func(t *testing.T) {
		err := threadExportCmd.Args(threadExportCmd, []string{})
		if err == nil {
			t.Fatal("expected error for zero args")
		}
	})

	t.Run("multiple args", func(t *testing.T) {
		err := threadExportCmd.Args(threadExportCmd, []string{"id1", "id2"})
		if err == nil {
			t.Fatal("expected error for multiple args")
		}
	})
}

func TestThreadExport_FileCreationError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o555); err != nil {
		t.Fatalf("create readonly dir: %v", err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0o755) }()

	outputFile := filepath.Join(readOnlyDir, "output.txt")

	oldStoreDir := viper.GetString("store.dir")
	oldOutput := viper.GetString("output")
	viper.Set("store.dir", tmpDir)
	viper.Set("output", outputFile)
	t.Cleanup(func() {
		viper.Set("store.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	err = threadExportCmd.RunE(threadExportCmd, []string{thr.ID})
	if err == nil {
		t.Fatal("expected error for file creation failure")
	}
	if !strings.Contains(err.Error(), "create output file") {
		t.Errorf("error message missing 'create output file': %v", err)
	}
}

func TestThreadExport_Stdout(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	t.Cleanup(func() {
		os.Stdout = oldStdout
		w.Close()
		r.Close()
	})

	oldStoreDir := viper.GetString("store.dir")
	oldOutput := viper.GetString("output")
	viper.Set("store.dir", tmpDir)
	viper.Set("output", "")
	t.Cleanup(func() {
		viper.Set("store.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	err = threadExportCmd.RunE(threadExportCmd, []string{thr.ID})
	if err != nil {
		t.Fatalf("threadExportCmd.RunE: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	r.Close()

	if !strings.Contains(buf.String(), thr.ID) {
		t.Errorf("stdout output missing thread ID: %s", buf.String())
	}
}

func TestThreadExport_FileOutput_Formats(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	formats := []string{"json", "html"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			outputFile := filepath.Join(t.TempDir(), "output."+format)

			oldStoreDir := viper.GetString("store.dir")
			oldOutput := viper.GetString("output")
			oldFormat := viper.GetString("format")
			viper.Set("store.dir", tmpDir)
			viper.Set("output", outputFile)
			viper.Set("format", format)
			t.Cleanup(func() {
				viper.Set("store.dir", oldStoreDir)
				viper.Set("output", oldOutput)
				viper.Set("format", oldFormat)
			})

			if err := threadExportCmd.RunE(threadExportCmd, []string{thr.ID}); err != nil {
				t.Fatalf("threadExportCmd.Execute: %v", err)
			}

			content, err := os.ReadFile(outputFile)
			if err != nil {
				t.Fatalf("read output file: %v", err)
			}

			switch format {
			case "json":
				if !strings.Contains(string(content), `"id"`) {
					t.Errorf("json output missing id field: %s", string(content))
				}
			case "html":
				if !strings.Contains(string(content), "<!DOCTYPE html>") {
					t.Errorf("html output missing DOCTYPE: %s", string(content))
				}
			}
		})
	}
}

func TestThreadAnalytics_StoreWide(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// Thread 1: a single text turn.
	thr1, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thr1.State.Append(state.RoleUser, artifact.Text{Content: "hi"})
	if err := store.Save(thr1); err != nil {
		t.Fatalf("save thread 1: %v", err)
	}

	// Thread 2: a reasoning turn plus a tool call turn.
	thr2, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thr2.State.Append(state.RoleAssistant, artifact.Reasoning{Content: "think"})
	thr2.State.Append(state.RoleAssistant, artifact.ToolCall{
		ID:        "call-1",
		Name:      "bash",
		Arguments: `{"cmd":"ls"}`,
	})
	if err := store.Save(thr2); err != nil {
		t.Fatalf("save thread 2: %v", err)
	}

	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(30, "", store, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore: %v", err)
	}

	output := buf.String()

	// Header columns.
	for _, col := range []string{"KIND", "SOURCE", "COUNT", "BYTES"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing header column %q: %s", col, output)
		}
	}

	// Each present kind must appear at least once.
	for _, kind := range []string{"text", "reasoning", "tool_call"} {
		if !strings.Contains(output, kind) {
			t.Errorf("output missing kind %q: %s", kind, output)
		}
	}

	// The lone tool_call was named "bash"; its Source column must
	// reflect that, so the user can attribute context cost to the
	// specific tool rather than just the kind.
	if !strings.Contains(output, "bash") {
		t.Errorf("output missing tool source %q for tool_call: %s", "bash", output)
	}

	// Thread 1: one text artifact, content "hi" -> 2 bytes.
	// Thread 2: one reasoning artifact, content "think" -> 5 bytes.
	// The tool_call LLMString for `{"cmd":"ls"}` is 12 bytes.
	// Assert those specific counts appear in the tabwriter output.
	for _, expect := range []string{"2", "5", "12"} {
		if !strings.Contains(output, expect) {
			t.Errorf("output missing expected byte count %q: %s", expect, output)
		}
	}
}

func TestThreadAnalytics_DaysFilter(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// Recent thread with a text artifact.
	recent, err := store.Create()
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.State.Append(state.RoleUser, artifact.Text{Content: "fresh"})
	if err := store.Save(recent); err != nil {
		t.Fatalf("save recent thread: %v", err)
	}

	// Old thread written as raw JSON with a 60-day-old timestamp.
	// The format must match the on-disk envelope shape produced by
	// junk/serialize.go (a {kind, data} wrapper around the artifact
	// body); otherwise junk.JSONStore silently skips the file.
	oldID := "00000000000000000000000000000001"
	oldTime := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	oldJSON := fmt.Sprintf(
		`{"id":"%s","created_at":"%s","updated_at":"%s","turns":[{"role":"user","artifacts":[{"kind":"text","data":{"kind":"text","content":"stale"}}],"timestamp":"%s"}]}`,
		oldID, oldTime, oldTime, oldTime,
	)
	oldPath := filepath.Join(tmpDir, oldID+".json")
	if err := os.WriteFile(oldPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old thread file: %v", err)
	}

	// Reload the store so it picks up the manually written file.
	store, err = junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	// With days=30, only the recent thread contributes.
	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(30, "", store, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore(30): %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "text") {
		t.Errorf("days=30 output missing text kind: %s", output)
	}
	if !strings.Contains(output, "5") {
		t.Errorf("days=30 output missing recent thread's byte count: %s", output)
	}

	// With days=90, both threads contribute, and the text row should
	// aggregate both contents (5 + 5 = 10 bytes, count 2).
	buf.Reset()
	if err := runThreadAnalyticsWithStore(90, "", store, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore(90): %v", err)
	}

	output = buf.String()
	if !strings.Contains(output, "10") {
		t.Errorf("days=90 output should aggregate both threads' bytes: %s", output)
	}
}

func TestThreadAnalytics_ThreadID(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr, err := store.Create()
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thr.State.Append(state.RoleUser, artifact.Text{Content: "one"})
	thr.State.Append(state.RoleUser, artifact.Text{Content: "two"})
	thr.State.Append(state.RoleAssistant, artifact.ToolCall{
		ID:        "call-1",
		Name:      "bash",
		Arguments: `{"cmd":"ls"}`,
	})
	if err := store.Save(thr); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(30, thr.ID, store, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore: %v", err)
	}

	output := buf.String()

	// Header columns (the Source column is asserted explicitly because
	// it is the new behavior under test — it must be present in the
	// output rather than silently dropped).
	for _, col := range []string{"KIND", "SOURCE", "COUNT", "BYTES"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing header column %q: %s", col, output)
		}
	}

	// Both kinds must appear.
	if !strings.Contains(output, "text") {
		t.Errorf("output missing text kind: %s", output)
	}
	if !strings.Contains(output, "tool_call") {
		t.Errorf("output missing tool_call kind: %s", output)
	}

	// The tool_call was named "bash"; its Source column must
	// reflect that, so the user can attribute context cost to the
	// specific tool rather than just the kind.
	if !strings.Contains(output, "bash") {
		t.Errorf("output missing tool source %q for tool_call: %s", "bash", output)
	}

	// Two text artifacts of length 3 each -> 6 bytes.
	if !strings.Contains(output, "6") {
		t.Errorf("output missing expected text bytes (6): %s", output)
	}
	// The tool_call LLMString for `{"cmd":"ls"}` is 12 bytes.
	if !strings.Contains(output, "12") {
		t.Errorf("output missing expected tool_call bytes (12): %s", output)
	}
}

func TestThreadAnalytics_ThreadNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := junk.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadAnalyticsWithStore(30, "nonexistent-id", store, &buf)
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("error message missing 'thread not found': %v", err)
	}
}
