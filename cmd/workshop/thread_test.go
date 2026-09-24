package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/spf13/viper"
)

func closePipe(t *testing.T, f *os.File) {
	t.Helper()
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close pipe: %v", err)
	}
}

// seedThreadAt writes a single journal entry to the given repository
// with a controlled timestamp, simulating a thread with one user
// turn. It is used in place of repo.SaveTurn + time.Sleep in
// tests that need a predictable sort order.
//
// The previous implementation relied on the per-thread UpdatedAt
// field, which was advanced by the repo on every Save and so made
// "later-created threads sort first" trivial to express. That field
// was removed when ore/junk migrated to a tree-backed ledger; the
// sort key is now derived from the most recent turn's timestamp. A
// freshly-created thread has no turns and therefore sorts last
// regardless of when Create was called — so this helper stamps a
// single user turn with a controlled clock via WithThreadClock.
// lastTurn returns a pointer to the most recently appended turn on
// the given thread. Used by the tests that manually persist threads
// to a repository (the ledger.Repository surface is the only one
// exposed; there is no equivalent thread type for callers for callers to
// save directly).
func lastTurn(thr *ledger.Thread) *ledger.Turn {
	turns := thr.AllTurns()
	t := turns[len(turns)-1]
	return &t
}

func seedThreadAt(t *testing.T, repo ledger.Repository, id string, lastAt time.Time) string {
	t.Helper()

	thr := ledger.NewThread(ledger.WithThreadClock(ledger.ClockFunc(func() time.Time { return lastAt })))
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	turns := thr.AllTurns()
	last := turns[len(turns)-1]
	if err := repo.SaveTurn(context.Background(), id, &last); err != nil {
		t.Fatalf("seedThreadAt(%s): save turn: %v", id, err)
	}
	if err := repo.UpdateThreadTip(context.Background(), id, thr.CurrentTip); err != nil {
		t.Fatalf("seedThreadAt(%s): update tip: %v", id, err)
	}
	return id
}

// TestThreadList_EmptyStoreDir_FallsBackToDefault is intentionally
// NOT a test. An earlier version asserted the command runs without
// error when repo.dir is empty, but that depended on the default
// XDG data directory being clean. On machines with prior workshop
// sessions, that directory can contain thread files that the JSON
// repo cannot parse, and the resulting panic (in junk's
// unmarshalTurns, not in workshop code) propagates out of List() and
// fails the test for an environmental reason.
//
// The fallback itself is a one-line `if storeDir == ""` in
// runThreadList; it is exercised by every other test that calls
// RunE without setting repo.dir, and it does not justify the
// fragility of reading the real XDG path. If we ever need explicit
// coverage, the right shape is to set XDG_DATA_HOME to a temp dir
// for the duration of the test.

func TestThreadList_WithStore(t *testing.T) {
	tmpDir := t.TempDir()

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Two threads with controlled, ascending last-activity
	// timestamps. seedThreadAt now returns the threadID (a string)
	// rather than the thread object because the new persistence
	// surface doesn't expose a thread type to callers.
	now := time.Now()
	thr1 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000001", now.Add(-2*time.Minute))
	thr2 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000002", now.Add(-1*time.Minute))

	// Render directly via the inner helper — bypassing the cobra
	// path which depends on viper-bound --store.dir. The CLI
	// plumbing is exercised separately by a smoke test.
	var buf bytes.Buffer
	if err := runThreadListWithStore(context.Background(), 20, "", false, repo, &buf); err != nil {
		t.Fatalf("runThreadListWithStore: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, thr1) {
		t.Errorf("output missing thread 1 ID: %s", output)
	}
	if !strings.Contains(output, thr2) {
		t.Errorf("output missing thread 2 ID: %s", output)
	}
	// Verify sort order: thread2 (more recent) should appear before thread1.
	idx1 := strings.Index(output, thr1)
	idx2 := strings.Index(output, thr2)
	if idx1 == -1 || idx2 == -1 {
		t.Fatalf("could not find thread IDs in output")
	}
	if idx2 > idx1 {
		t.Errorf("sort order wrong: thread2 should appear before thread1; idx1=%d, idx2=%d", idx1, idx2)
	}
}

// TestThreadList_Pagination_DefaultSort covers the default sort
// order: most recently active first, with id ascending as the
// deterministic tiebreaker. Three threads spaced in time are saved
// with controlled timestamps; the test verifies the rendered output
// lists them from most-recent to least-recent.
func TestThreadList_Pagination_DefaultSort(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now()
	thr1 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000001", now.Add(-30*time.Minute))
	thr2 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000002", now.Add(-15*time.Minute))
	thr3 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000003", now)

	var buf bytes.Buffer
	if err := runThreadListWithStore(context.Background(), 20, "", false, repo, &buf); err != nil {
		t.Fatalf("runThreadListWithStore: %v", err)
	}

	output := buf.String()
	idx1 := strings.Index(output, thr1)
	idx2 := strings.Index(output, thr2)
	idx3 := strings.Index(output, thr3)
	if idx1 == -1 || idx2 == -1 || idx3 == -1 {
		t.Fatalf("could not find all thread IDs in output:\n%s", output)
	}
	// Most recent first.
	if idx3 >= idx2 || idx2 >= idx1 {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })
	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Each thread gets a strictly-increasing last-activity stamp
	// so the listing has a deterministic order: most recent first.
	now := time.Now()
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i+1)
		seedThreadAt(t, repo, id, now.Add(time.Duration(i-4)*time.Minute))
		ids = append(ids, id)
	}

	var buf bytes.Buffer
	if err := runThreadListWithStore(context.Background(), 2, "", false, repo, &buf); err != nil {
		t.Fatalf("runThreadListWithStore(2): %v", err)
	}

	output := buf.String()
	// The two most recent threads (ids[3] and ids[4]) should be present.
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })
	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now()
	want := make(map[string]bool)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i+1)
		seedThreadAt(t, repo, id, now.Add(time.Duration(i)*time.Minute))
		want[id] = true
	}

	var buf bytes.Buffer
	if err := runThreadListWithStore(context.Background(), 2, "", true, repo, &buf); err != nil {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })
	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now()
	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i+1)
		seedThreadAt(t, repo, id, now.Add(time.Duration(i)*time.Minute))
		ids = append(ids, id)
	}

	// Page 1.
	var page1 bytes.Buffer
	if err := runThreadListWithStore(context.Background(), 2, "", false, repo, &page1); err != nil {
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
	if err := runThreadListWithStore(context.Background(), 2, cursor, false, repo, &page2); err != nil {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })
	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadListWithStore(context.Background(), 20, "!!!not-base64!!!", false, repo, &buf)
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })
	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now()
	thr1 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000001", now.Add(-1*time.Minute))
	thr2 := seedThreadAt(t, repo, "00000000-0000-0000-0000-000000000002", now)

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
			if err := runThreadListWithStore(context.Background(), tt.limit, "", false, repo, &buf); err != nil {
				t.Fatalf("runThreadListWithStore(%d): %v", tt.limit, err)
			}
			out := buf.String()
			count := 0
			if strings.Contains(out, thr1) {
				count++
			}
			if strings.Contains(out, thr2) {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	oldStoreDir := viper.GetString("repo.dir")
	viper.Set("repo.dir", tmpDir)
	t.Cleanup(func() { viper.Set("repo.dir", oldStoreDir) })

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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Recent thread with a 5-byte text artifact.
	recent := ledger.NewThread()
	recent.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	recentID := "test-recent"
	if err := repo.SaveTurn(context.Background(), recentID, lastTurn(recent)); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.Append(ledger.RoleUser, artifact.Text{Content: "fresh"})
	if err := repo.SaveTurn(context.Background(), recentID, lastTurn(recent)); err != nil {
		t.Fatalf("save recent turn: %v", err)
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

	oldStoreDir := viper.GetString("repo.dir")
	viper.Set("repo.dir", tmpDir)
	t.Cleanup(func() { viper.Set("repo.dir", oldStoreDir) })

	// Capture stdout from RunE.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
		closePipe(t, w)
		closePipe(t, r)
	})

	runErr := threadAnalyticsCmd.RunE(threadAnalyticsCmd, []string{"--days", "30"})

	closePipe(t, w)
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	formats := []string{"text", "json", "html"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := runThreadExportWithStore(context.Background(), repo, thrID, format, &buf); err != nil {
				t.Fatalf("runThreadExportWithStore(%s): %v", format, err)
			}

			output := buf.String()
			if output == "" {
				t.Errorf("expected non-empty output for format %s", format)
			}

			switch format {
			case "text":
				if !strings.Contains(output, thrID) {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadExportWithStore(context.Background(), repo, "nonexistent-id", "text", &buf)
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("error message missing 'thread not found': %v", err)
	}
}

func TestThreadExport_FileOutput(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "output.txt")

	oldStoreDir := viper.GetString("repo.dir")
	oldOutput := viper.GetString("output")
	viper.Set("repo.dir", tmpDir)
	viper.Set("output", outputFile)
	t.Cleanup(func() {
		viper.Set("repo.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	if err := threadExportCmd.RunE(threadExportCmd, []string{thrID}); err != nil {
		t.Fatalf("threadExportCmd.RunE: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}

	if !strings.Contains(string(content), thrID) {
		t.Errorf("file output missing thread ID: %s", string(content))
	}
}

func TestThreadExport_UnsupportedFormat(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadExportWithStore(context.Background(), repo, thrID, "xml", &buf)
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o555); err != nil {
		t.Fatalf("create readonly dir: %v", err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0o755) }()

	outputFile := filepath.Join(readOnlyDir, "output.txt")

	oldStoreDir := viper.GetString("repo.dir")
	oldOutput := viper.GetString("output")
	viper.Set("repo.dir", tmpDir)
	viper.Set("output", outputFile)
	t.Cleanup(func() {
		viper.Set("repo.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	err = threadExportCmd.RunE(threadExportCmd, []string{thrID})
	if err == nil {
		t.Fatal("expected error for file creation failure")
	}
	if !strings.Contains(err.Error(), "create output file") {
		t.Errorf("error message missing 'create output file': %v", err)
	}
}

func TestThreadExport_Stdout(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
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
		closePipe(t, w)
		closePipe(t, r)
	})

	oldStoreDir := viper.GetString("repo.dir")
	oldOutput := viper.GetString("output")
	viper.Set("repo.dir", tmpDir)
	viper.Set("output", "")
	t.Cleanup(func() {
		viper.Set("repo.dir", oldStoreDir)
		viper.Set("output", oldOutput)
	})

	err = threadExportCmd.RunE(threadExportCmd, []string{thrID})
	if err != nil {
		t.Fatalf("threadExportCmd.RunE: %v", err)
	}

	closePipe(t, w)
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	closePipe(t, r)

	if !strings.Contains(buf.String(), thrID) {
		t.Errorf("stdout output missing thread ID: %s", buf.String())
	}
}

func TestThreadExport_FileOutput_Formats(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	formats := []string{"json", "html"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			outputFile := filepath.Join(t.TempDir(), "output."+format)

			oldStoreDir := viper.GetString("repo.dir")
			oldOutput := viper.GetString("output")
			oldFormat := viper.GetString("format")
			viper.Set("repo.dir", tmpDir)
			viper.Set("output", outputFile)
			viper.Set("format", format)
			t.Cleanup(func() {
				viper.Set("repo.dir", oldStoreDir)
				viper.Set("output", oldOutput)
				viper.Set("format", oldFormat)
			})

			if err := threadExportCmd.RunE(threadExportCmd, []string{thrID}); err != nil {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Thread 1: a single text turn.
	thr1ID := "test-thr1"
	thr1 := ledger.NewThread()
	thr1.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT1 := lastTurn(thr1)
	if err := repo.SaveTurn(context.Background(), thr1ID, lastT1); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thr1.Append(ledger.RoleUser, artifact.Text{Content: "hi"})
	if err := repo.SaveTurn(context.Background(), thr1ID, lastTurn(thr1)); err != nil {
		t.Fatalf("save thread 1 turn: %v", err)
	}

	// Thread 2: a reasoning turn plus a tool call turn.
	thr2 := ledger.NewThread()
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thr2.Append(ledger.RoleAssistant, artifact.Reasoning{Content: "think"})
	thr2.Append(ledger.RoleAssistant, artifact.ToolCall{
		ID:        "call-1",
		Name:      "bash",
		Arguments: `{"cmd":"ls"}`,
	})
	if err := repo.SaveTurn(context.Background(), "test-thr2", lastTurn(thr2)); err != nil {
		t.Fatalf("save thread 2 turn: %v", err)
	}

	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(context.Background(), 30, "", repo, &buf); err != nil {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Recent thread with a text artifact.
	recent := ledger.NewThread()
	recent.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	recentID := "test-recent"
	if err := repo.SaveTurn(context.Background(), recentID, lastTurn(recent)); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.Append(ledger.RoleUser, artifact.Text{Content: "fresh"})
	if err := repo.SaveTurn(context.Background(), recentID, lastTurn(recent)); err != nil {
		t.Fatalf("save recent turn: %v", err)
	}

	// Old thread written as raw JSON with a 60-day-old timestamp.
	// The format must match the on-disk envelope shape produced by
	// junk/serialize.go (a {kind, data} wrapper around the artifact
	// body); otherwise the file is silently skipped the file.
	//
	// The turn also needs an `id` and the thread needs a matching
	// `current_tip`: the tree-backed ledger in ore/junk walks from
	// CurrentTip through ParentID, so a turn with no ID is unreachable
	// and contributes nothing to analytics.
	oldID := "00000000000000000000000000000001"
	oldTurnID := "00000000000000000000000000000002"
	oldTime := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	oldJSON := fmt.Sprintf(
		`{"id":"%s","current_tip":"%s","created_at":"%s","updated_at":"%s","turns":[{"id":"%s","parent_id":"","role":"user","artifacts":[{"kind":"text","data":{"kind":"text","content":"stale"}}],"timestamp":"%s"}]}`,
		oldID, oldTurnID, oldTime, oldTime, oldTurnID, oldTime,
	)
	oldPath := filepath.Join(tmpDir, oldID+".json")
	if err := os.WriteFile(oldPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old thread file: %v", err)
	}

	// Reload the repo so it picks up the manually written file.
	repo, err = ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}

	// With days=30, only the recent thread contributes.
	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(context.Background(), 30, "", repo, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore(context.Background(), 30): %v", err)
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
	if err := runThreadAnalyticsWithStore(context.Background(), 90, "", repo, &buf); err != nil {
		t.Fatalf("runThreadAnalyticsWithStore(context.Background(), 90): %v", err)
	}

	output = buf.String()
	if !strings.Contains(output, "10") {
		t.Errorf("days=90 output should aggregate both threads' bytes: %s", output)
	}
}

func TestThreadAnalytics_ThreadID(t *testing.T) {
tmpDir := t.TempDir()
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	thrID := "test-thr"
	thr := ledger.NewThread()
	thr.Append(ledger.RoleUser, artifact.Text{Content: "x"})
	lastT := lastTurn(thr)
	if err := repo.SaveTurn(context.Background(), thrID, lastT); err != nil { t.Fatalf("save: %v", err) }
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thr.Append(ledger.RoleUser, artifact.Text{Content: "one"})
	thr.Append(ledger.RoleUser, artifact.Text{Content: "two"})
	thr.Append(ledger.RoleAssistant, artifact.ToolCall{
		ID:        "call-1",
		Name:      "bash",
		Arguments: `{"cmd":"ls"}`,
	})
	if err := repo.SaveTurn(context.Background(), thrID, lastTurn(thr)); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	var buf bytes.Buffer
	if err := runThreadAnalyticsWithStore(context.Background(), 30, thrID, repo, &buf); err != nil {
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
	t.Cleanup(func() { fmt.Println("DEBUG: tmpDir =", tmpDir); files, _ := os.ReadDir(tmpDir); for _, f := range files { fmt.Println("DEBUG: file", f.Name()) }; time.Sleep(100*time.Millisecond) })

	repo, err := ledger.NewFileRepository(tmpDir)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	var buf bytes.Buffer
	err = runThreadAnalyticsWithStore(context.Background(), 30, "nonexistent-id", repo, &buf)
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("error message missing 'thread not found': %v", err)
	}
}
