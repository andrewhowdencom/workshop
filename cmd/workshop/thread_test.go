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

	"github.com/andrewhowdencom/ore/session"
	"github.com/spf13/viper"
)

func TestThreadList_EmptyStoreDir(t *testing.T) {
	oldStoreDir := viper.GetString("store.dir")
	viper.Set("store.dir", "")
	t.Cleanup(func() { viper.Set("store.dir", oldStoreDir) })

	// When store.dir is empty, the command should fall back to the default
	// XDG data directory rather than erroring.
	if err := threadListCmd.RunE(threadListCmd, []string{}); err != nil {
		t.Fatalf("expected no error for empty store.dir, got %v", err)
	}
}

func TestThreadList_WithStore(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := session.NewJSONStore(tmpDir)
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

func TestThreadList_DaysFilter(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := session.NewJSONStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	recent, err := store.Create()
	if err != nil {
		t.Fatalf("create recent thread: %v", err)
	}
	recent.Metadata["workshop.role"] = "recent"
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
	store, err = session.NewJSONStore(tmpDir)
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

func TestThreadExport_Success(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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

	store, err := session.NewJSONStore(tmpDir)
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
