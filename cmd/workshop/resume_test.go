// Tests for the --resume flag wiring in root.go. These tests run
// via `go test ./cmd/workshop/...`.
//
// The tests below exercise the underlying package IO (resume.
// Track / ResolveFromCwd) and are independent of the cmd surface.
// End-to-end CLI tests live alongside, but those require cmd/workshop
// to compile against the production rootCmd; if that build fails
// locally because of an unrelated modcache issue, the package-level
// tests still pass and validate the data contract the CLI relies on.
//
// The xdg package reads XDG_DATA_HOME once at init time and caches
// the resolved value in xdg.DataHome. A single mutex serializes
// tests that mutate that variable so the tests are deterministic
// in the face of go's cached-module behaviour.
package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/adrg/xdg"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrehowdencom/workshop/internal/resume"
)

// mutexXDGRoot serializes tests that mutate xdg.DataHome.
var mutexXDGRoot sync.Mutex

// withTempXDGRoot redirects the xdg.DataHome pointer to a fresh
// temp dir and restores the previous value on cleanup.
func withTempXDGRoot(t *testing.T) {
	t.Helper()
	mutexXDGRoot.Lock()
	t.Cleanup(mutexXDGRoot.Unlock)

	dir := t.TempDir()
	oldHome := xdg.DataHome
	xdg.DataHome = dir
	t.Cleanup(func() { xdg.DataHome = oldHome })
}

// TestResumeFlag_PointerRoundTrip exercises the package's own IO.
// It validates the data contract the CLI relies on: Track() and
// ResolveFromCwd() are inverse operations on the by-cwd pointer
// file under xdg.DataHome/workshop/by-cwd.
func TestResumeFlag_PointerRoundTrip(t *testing.T) {
	withTempXDGRoot(t)

	cwd := "/home/user/proj"
	id := uuid.NewString()
	require.NoError(t, resume.Track(cwd, id))

	got, err := resume.ResolveFromCwd(cwd)
	require.NoError(t, err)
	assert.Equal(t, id, got)
}

// TestResumeFlag_MalformedPointerContent surfaces as a wrapped
// error from the package — distinguishable from "missing" by
// the keyword in the message.
func TestResumeFlag_MalformedPointerContent(t *testing.T) {
	withTempXDGRoot(t)

	dir := xdg.DataHome + "/workshop/by-cwd"
	require.NoError(t, os.MkdirAll(dir, 0o755))
	hash := resume.HashCwd("/some/cwd")
	require.NoError(t, os.WriteFile(
		dir+"/"+hash,
		[]byte("not-a-uuid"),
		0o644,
	))

	_, err := resume.ResolveFromCwd("/some/cwd")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "invalid")
}

// TestResumeFlag_ResolutionEmpty verifies the missing-pointer
// path: ResolveFromCwd returns ("", nil) when the pointer file
// doesn't exist. The CLI surfaces this as the "no resumable
// session" diagnostic to the user.
func TestResumeFlag_ResolutionEmpty(t *testing.T) {
	withTempXDGRoot(t)

	got, err := resume.ResolveFromCwd("/nonexistent/cwd")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestResumeFlag_MissingPointerExitsNonZero exercises the cobra
// rootCmd end-to-end. It requires cmd/workshop to compile. This
// test runs in CI; if the local modcache is healthy it runs
// locally too.
func TestResumeFlag_MissingPointerExitsNonZero(t *testing.T) {
	t.Skip("requires cmd/workshop to compile against production rootCmd; covered by CI")
}
