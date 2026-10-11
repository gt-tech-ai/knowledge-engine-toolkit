package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
)

// TestOSFS_RoundTrips tests that OSFS faithfully delegates the full filesystem
// surface to the OS against a real temp directory.
//
// Why this test is important:
//   - OSFS is the production FS implementation injected wherever code touches the
//     filesystem; a broken delegation (wrong os call, swallowed error) would
//     corrupt real reads/writes across every service. Exercising it against a
//     temp dir proves the behavior end-to-end without mocking the unit under test.
//
// What it tests:
//   - WriteFile then ReadFile round-trips bytes; MkdirAll + Stat report a dir;
//     ReadDir lists entries; Rename moves a file (old path gone, new path present);
//     RemoveAll deletes a tree.
func TestOSFS_RoundTrips(t *testing.T) {
	t.Parallel()
	fs := fsys.NewOSFS()
	dir := t.TempDir()

	sub := filepath.Join(dir, "a", "b")
	require.NoError(t, fs.MkdirAll(sub, 0o755))
	info, err := fs.Stat(sub)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "MkdirAll + Stat report a directory")

	file := filepath.Join(sub, "f.txt")
	want := []byte("hello fsys")
	require.NoError(t, fs.WriteFile(file, want, 0o644))
	got, err := fs.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, want, got, "WriteFile -> ReadFile round-trips bytes")

	entries, err := fs.ReadDir(sub)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "f.txt", entries[0].Name(), "ReadDir lists the written file")

	moved := filepath.Join(sub, "g.txt")
	require.NoError(t, fs.Rename(file, moved))
	_, err = fs.Stat(file)
	require.Error(t, err, "old path is gone after Rename")
	_, err = fs.Stat(moved)
	assert.NoError(t, err, "new path exists after Rename")

	require.NoError(t, fs.RemoveAll(filepath.Join(dir, "a")))
	_, err = fs.Stat(sub)
	assert.True(t, os.IsNotExist(err), "RemoveAll deletes the tree")
}

// TestOSFS_Constructors tests that the package constructors return a usable
// production FS.
//
// Why this test is important:
//   - DefaultFS/NewOSFS are the injection seams callers use to obtain the
//     production FS; if either returned nil or a non-functional value, every
//     consumer would panic at first use.
//
// What it tests:
//   - DefaultFS() and NewOSFS() return non-nil FS values that can Stat a real path.
func TestOSFS_Constructors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, fs := range map[string]fsys.FS{
		"DefaultFS": fsys.DefaultFS(),
		"NewOSFS":   fsys.NewOSFS(),
	} {
		require.NotNil(t, fs, name)
		_, err := fs.Stat(dir)
		assert.NoError(t, err, name)
	}
}
