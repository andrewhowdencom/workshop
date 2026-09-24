package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	cmdErr := versionCmd.RunE(versionCmd, []string{})

	closeErr := w.Close()
	os.Stdout = oldStdout
	if closeErr != nil {
		t.Fatalf("close stdout pipe: %v", closeErr)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close pipe reader: %v", err)
		}
	}()

	if cmdErr != nil {
		t.Fatalf("version RunE failed: %v", cmdErr)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	output := strings.TrimSpace(buf.String())
	if output == "" {
		t.Fatal("version command produced empty output")
	}
}
