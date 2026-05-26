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

	"github.com/andrewhowdencom/ore/thread"
	"github.com/spf13/viper"
)

func TestThreadList_EmptyStoreDir(t *testing.T) {
	oldStoreDir := viper.GetString("store.dir")
	viper.Set("store.dir", "")
	t.Cleanup(func() { viper.Set("store.dir", oldStoreDir) })

	err := threadListCmd.RunE(threadListCmd, []string{})
	if err == nil {
		t.Fatal("expected error for empty store.dir, got nil")
	}
}

func TestThreadList_WithStore(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := thread.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	thr1, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thr1.SetMetadata("workshop.role", "developer")
	if err := store.Save(thr1); err != nil {
		t.Fatalf("save thread 1: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	thr2, err := store.Create()
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thr2.SetMetadata("workshop.role", "reviewer")
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

func TestThreadList_DaysFilter(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := thread.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	recent, err := store.Create()
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.SetMetadata("workshop.role", "recent")
	if err := store.Save(recent); err != nil {
		t.Fatalf("save recent thread: %v", err)
	}

	// Create an old thread by writing JSON directly with an older timestamp.
	oldID := "00000000000000000000000000000001"
	oldTime := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	oldJSON := fmt.Sprintf(
		`{"id":"%s","created_at":"%s","updated_at":"%s","metadata":{"workshop.role":"old"},"turns":[]}`,
		oldID, oldTime, oldTime,
	)
	oldPath := filepath.Join(tmpDir, oldID+".json")
	if err := os.WriteFile(oldPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old thread file: %v", err)
	}

	// Reload the store to pick up the manually created old thread.
	store, err = thread.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	// With days=30, only the recent thread should appear.
	var buf bytes.Buffer
	if err := runThreadListWithStore(30, store, &buf); err != nil {
		t.Fatalf("runThreadListWithStore(30): %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, recent.ID) {
		t.Errorf("output missing recent thread: %s", output)
	}
	if strings.Contains(output, oldID) {
		t.Errorf("output should not contain old thread: %s", output)
	}

	// With days=90, both threads should appear.
	buf.Reset()
	if err := runThreadListWithStore(90, store, &buf); err != nil {
		t.Fatalf("runThreadListWithStore(90): %v", err)
	}

	output = buf.String()
	if !strings.Contains(output, recent.ID) {
		t.Errorf("output missing recent thread with days=90: %s", output)
	}
	if !strings.Contains(output, oldID) {
		t.Errorf("output missing old thread with days=90: %s", output)
	}
}
