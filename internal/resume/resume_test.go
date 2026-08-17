package resume

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/adrg/xdg"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempXDGRoot redirects the by-cwd data directory to a fresh temp
// dir so tests cannot pollute the user's real pointer directory, and
// restores xdg.DataHome on test cleanup.
//
// The xdg package reads XDG_DATA_HOME once at init time and caches the
// resolved value in the xdg.DataHome package variable. Setting
// XDG_DATA_HOME later via t.Setenv does not affect that cached value,
// so this helper mutates the variable directly. The t.Cleanup hook
// restores the previous value so subsequent tests are not affected.
func withTempXDGRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := xdg.DataHome
	xdg.DataHome = dir
	t.Cleanup(func() { xdg.DataHome = old })
	return dir
}

func TestHashCwd_StabilityAcrossNormalization(t *testing.T) {
	// Equivalent paths (with trailing slash, ./, repeated separators)
	// must hash to the same value, mirroring defaultMeta's shortCwd.
	cases := []string{
		"/home/user/proj",
		"/home/user/proj/",
		"/home/user/./proj",
		"/home/user//proj",
	}
	first := HashCwd(cases[0])
	for _, c := range cases[1:] {
		assert.Equal(t, first, HashCwd(c), "HashCwd(%q) should match HashCwd(%q)", c, cases[0])
	}
}

func TestHashCwd_Distinct(t *testing.T) {
	// Distinct paths must hash to distinct values (sanity check — the
	// hash function is SHA-256 so this is overwhelmingly likely, but
	// the test guards against an accidental constant-return refactor).
	assert.NotEqual(t, HashCwd("/home/user/proj"), HashCwd("/home/user/other"))
}

func TestResolveFromCwd_Missing(t *testing.T) {
	withTempXDGRoot(t)

	id, err := ResolveFromCwd("/tmp/nowhere")
	require.NoError(t, err, "missing pointer is not an error")
	assert.Equal(t, "", id, "missing pointer resolves to empty id")
}

func TestTrack_ThenResolve_RoundTrip(t *testing.T) {
	withTempXDGRoot(t)

	cwd := "/home/user/proj"
	threadID := uuid.NewString()
	require.NoError(t, Track(cwd, threadID))

	got, err := ResolveFromCwd(cwd)
	require.NoError(t, err)
	assert.Equal(t, threadID, got)
}

func TestTrack_IsIdempotent(t *testing.T) {
	withTempXDGRoot(t)

	cwd := "/home/user/proj"
	a := uuid.NewString()
	b := uuid.NewString()
	require.NoError(t, Track(cwd, a))
	require.NoError(t, Track(cwd, b))

	got, err := ResolveFromCwd(cwd)
	require.NoError(t, err)
	assert.Equal(t, b, got, "second Track should overwrite the first")
}

func TestResolveFromCwd_MalformedContent(t *testing.T) {
	dir := withTempXDGRoot(t)

	cwd := "/home/user/proj"
	// Write a malformed pointer directly to disk, bypassing Track so
	// we exercise the read-side validator.
	pointerDir := filepath.Join(dir, "workshop", "by-cwd")
	require.NoError(t, os.MkdirAll(pointerDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pointerDir, HashCwd(cwd)),
		[]byte("not-a-uuid\n"),
		0o644,
	))

	_, err := ResolveFromCwd(cwd)
	require.Error(t, err)
	assert.ErrorIs(t, err, errInvalidPointer)
}

func TestResolveFromCwd_EmptyFile(t *testing.T) {
	dir := withTempXDGRoot(t)

	cwd := "/home/user/proj"
	pointerDir := filepath.Join(dir, "workshop", "by-cwd")
	require.NoError(t, os.MkdirAll(pointerDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pointerDir, HashCwd(cwd)),
		[]byte{},
		0o644,
	))

	id, err := ResolveFromCwd(cwd)
	require.NoError(t, err, "empty file is treated as missing, not as an error")
	assert.Equal(t, "", id)
}

func TestTrack_RejectsInvalidUUID(t *testing.T) {
	withTempXDGRoot(t)

	err := Track("/home/user/proj", "not-a-uuid")
	assert.Error(t, err, "Track must refuse non-UUID thread ids")
}

func TestTrack_AtomicUnderConcurrentWriters(t *testing.T) {
	// Two goroutines Track the same cwd with distinct UUIDs. After
	// the dust settles, ResolveFromCwd must return one of the two
	// values — never a torn file (which would fail UUID validation
	// and surface as errInvalidPointer).
	withTempXDGRoot(t)

	cwd := "/home/user/proj"
	a := uuid.NewString()
	b := uuid.NewString()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		assert.NoError(t, Track(cwd, a))
	}()
	go func() {
		defer wg.Done()
		assert.NoError(t, Track(cwd, b))
	}()
	wg.Wait()

	got, err := ResolveFromCwd(cwd)
	require.NoError(t, err, "torn file would surface here as errInvalidPointer")
	assert.True(t, got == a || got == b, "got %q, want one of %q %q", got, a, b)
}

func TestPointerDir_CreatesIfMissing(t *testing.T) {
	withTempXDGRoot(t)

	dir, err := PointerDir()
	require.NoError(t, err)
	assert.DirExists(t, dir)

	// Second call must be a no-op (idempotent).
	_, err = PointerDir()
	require.NoError(t, err)
}

func TestFormatHint(t *testing.T) {
	hint := FormatHint()
	assert.Contains(t, hint, "workshop --resume", "hint must contain the command")
	assert.Contains(t, hint, "To resume this session:", "hint must contain a prose label")

	// The command should be on its own line and indented, so a
	// triple-click on the second line selects just the command.
	lines := splitLines(hint)
	require.Len(t, lines, 2, "hint should be exactly two lines")
	assert.True(t, strings.HasPrefix(lines[1], " "),
		"second line %q should start with whitespace (indented for triple-click selection)", lines[1])
	assert.Equal(t, "  workshop --resume", lines[1],
		"second line should be the indented command exactly")
}

// splitLines is a small local helper (rather than importing strings)
// so the test file is grep-friendly when investigating the format.
func splitLines(s string) []string {
	out := []string{}
	for s != "" {
		i := indexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:i])
		s = s[i+1:]
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
