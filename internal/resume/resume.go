// Package resume owns the per-cwd pointer file used by the
// `workshop --resume` flag. The pointer file maps a directory to the
// most recent thread ID that ran there, so the user can quit the TUI
// and come back to the same conversation without remembering a UUID.
//
// The pointer file lives at
//
//	$XDG_DATA_HOME/workshop/by-cwd/<sha256hex(cwd)>
//
// where `cwd` is `filepath.Clean(os.Getwd())` — same normalization
// that `defaultMeta` uses for the session's `cwd` metadata, so the
// read and write paths agree on the key.
//
// The package is intentionally tiny: it owns three IO operations
// (read, write, hint format) and nothing else. Higher-level concerns
// (cobra wiring, store-existence checks, TUI exit hooks) live in
// `cmd/workshop` and `internal/app`.
package resume

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/google/uuid"
)

// PointerDirRel is the directory under $XDG_DATA_HOME that holds the
// per-cwd pointer files. Exposed so tests can construct equivalent
// paths without round-tripping through the XDG layer.
const PointerDirRel = "workshop/by-cwd"

// errInvalidPointer is returned by ResolveFromCwd when the pointer
// file exists but does not contain a parseable UUID. Callers should
// surface this as a corruption / user-error path, not as "no pointer".
var errInvalidPointer = errors.New("resume pointer: invalid contents")

// HashCwd returns the hex-encoded SHA-256 of filepath.Clean(cwd).
// Exposed so callers can show the file path in error messages without
// round-tripping the same normalization.
//
// Two distinct cwds that differ only in trailing separators, `./`
// components, or repeated separators all hash to the same value,
// which is the same convention the TUI's `defaultMeta` uses to seed
// the session's `cwd` metadata.
func HashCwd(cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])
}

// PointerDir returns the absolute path to the by-cwd pointer directory,
// creating it (mode 0755) if it does not yet exist. The directory is
// derived from $XDG_DATA_HOME via xdg.DataHome (the package-level
// variable populated at init time from the XDG_DATA_HOME env var).
// Idempotent.
//
// Tests that need a hermetic data home should mutate xdg.DataHome
// directly in a setup helper and restore it via t.Cleanup. The
// `t.Setenv("XDG_DATA_HOME", ...)` pattern does not work for this
// package because xdg caches its base directories at init time.
func PointerDir() (string, error) {
	dir := filepath.Join(xdg.DataHome, PointerDirRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create resume pointer dir: %w", err)
	}
	return dir, nil
}

// pointerPath returns the absolute path of the pointer file for cwd.
func pointerPath(cwd string) (string, error) {
	dir, err := PointerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, HashCwd(cwd)), nil
}

// ResolveFromCwd returns the most recent thread ID for the given cwd.
// Returns ("", nil) when no pointer file exists; this is the
// "nothing to resume" path and is not an error. Returns a wrapped
// errInvalidPointer when the file exists but cannot be parsed as a
// UUID — the caller should surface that distinctly (the user may
// have corrupted the file by hand).
//
// Other errors (e.g. permission denied on the by-cwd directory) are
// returned verbatim.
func ResolveFromCwd(cwd string) (string, error) {
	path, err := pointerPath(cwd)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read resume pointer: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		// Treat an empty pointer file the same as a missing one; it
		// can only happen if a previous Track wrote zero bytes, which
		// is not currently possible but the fallback is cheap.
		return "", nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("%w: %q", errInvalidPointer, id)
	}
	return id, nil
}

// Track writes the pointer file mapping cwd -> threadID. Idempotent:
// every call overwrites the previous value. Uses an atomic
// write-temp-then-rename so a concurrent reader never observes a
// torn file. The temp file is created via os.CreateTemp so concurrent
// Track calls in the same process do not collide on the temp path
// (which would happen if the temp name were keyed on os.Getpid alone
// — multiple goroutines in one process share a PID).
//
// Returns an error if the by-cwd directory cannot be created, the
// temp file cannot be written, or the rename fails.
func Track(cwd, threadID string) error {
	if _, err := uuid.Parse(threadID); err != nil {
		return fmt.Errorf("write resume pointer: invalid thread id %q: %w", threadID, err)
	}
	path, err := pointerPath(cwd)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".resume-pointer.tmp.*")
	if err != nil {
		return fmt.Errorf("create resume pointer temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write([]byte(threadID + "\n")); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write resume pointer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close resume pointer temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		// Best-effort: CreateTemp produces mode 0600. We want 0644
		// to match the rest of the data directory, but chmod failure
		// (e.g. on a filesystem that ignores mode bits) shouldn't
		// block the pointer write itself.
		_ = err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename resume pointer: %w", err)
	}
	return nil
}

// FormatHint returns the user-facing resume hint string. Two lines:
// a prose label, then the indented command on its own line so a
// triple-click selects just the command.
//
// Stable: callers can substring-match on this for tests, but should
// not parse it.
func FormatHint() string {
	return "To resume this session:\n  workshop --resume"
}
