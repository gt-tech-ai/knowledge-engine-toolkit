package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/workspace/python"
)

// pyRoots are the Python package roots the discovery cases search.
func pyRoots() []string { return []string{"libs/python", "services/python", "tools"} }

// TestPythonPackages tests discovery of Python packages by pyproject.toml.
//
// Why this test is important:
//   - A Python test/lint fan-out is driven by this discovery; a missed package (or a
//     false positive on a dir without pyproject.toml) would skip or break its checks.
//
// What it tests:
//   - Directories holding a pyproject.toml under the roots come back relative to the
//     repository, directly and nested (discovery recurses through a dir without one);
//     dirs without one and stray files are skipped; the filter narrows the result; a
//     missing root is tolerated, and an empty repository yields none.
func TestPythonPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(
		t,
		filepath.Join(root, "libs", "python", "shelf", "pyproject.toml"),
		"[project]\n",
	)
	wsWrite(
		t,
		filepath.Join(root, "services", "python", "lend", "pyproject.toml"),
		"[project]\n",
	)
	wsWrite(
		t,
		filepath.Join(root, "services", "python", "server", "deep", "pyproject.toml"),
		"[project]\n",
	)
	wsWrite(t, filepath.Join(root, "libs", "python", "nope", "readme.md"), "no pyproject")
	wsWrite(t, filepath.Join(root, "libs", "python", "stray.txt"), "x")

	pkgs, err := python.Packages(os.DirFS(root), pyRoots(), nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"libs/python/shelf", "services/python/lend", "services/python/server/deep",
	}, pkgs)

	filtered, err := python.Packages(os.DirFS(root), pyRoots(), func(rel string) bool {
		return strings.HasPrefix(rel, "libs/")
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"libs/python/shelf"}, filtered)

	none, err := python.Packages(os.DirFS(t.TempDir()), pyRoots(), nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestPythonPackages_Errors tests that discovery surfaces an unreadable tree.
//
// Why this test is important:
//   - Discovery feeds Python lint/typecheck/test fan-outs; a root that cannot be read,
//     or a nested tree that cannot be walked, must fail rather than silently drop
//     packages.
//
// What it tests:
//   - A root that is a file returns an error.
//   - An unreadable nested directory propagates the walk error (skipped as root, which
//     ignores directory permissions).
func TestPythonPackages_Errors(t *testing.T) {
	t.Parallel()

	t.Run("root that is a file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		wsWrite(t, filepath.Join(root, "tools"), "not a dir")
		_, err := python.Packages(os.DirFS(root), pyRoots(), nil)
		require.Error(t, err)
	})

	t.Run("unreadable nested directory", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		root := t.TempDir()
		bad := filepath.Join(root, "libs", "python", "shelf", "sub")
		require.NoError(t, os.MkdirAll(bad, 0o755))
		require.NoError(t, os.Chmod(bad, 0o000))
		t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })
		_, err := python.Packages(os.DirFS(root), pyRoots(), nil)
		require.Error(t, err)
	})
}

// TestPythonModuleName tests the importable module of a package directory.
//
// Why this test is important:
//   - Test and doc runs import a package by its module; a src-layout package's module
//     is the directory under src/, not the package directory's name, and build
//     artifacts there must not be mistaken for it.
//
// What it tests:
//   - A src/ layout yields its first real directory, skipping __pycache__ and
//     *.egg-info; without src/ the package directory's base name is used.
func TestPythonModuleName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, d := range []string{"__pycache__", "shelf.egg-info", "shelf_core"} {
		require.NoError(
			t,
			os.MkdirAll(filepath.Join(root, "libs", "shelf", "src", d), 0o755),
		)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "libs", "flat"), 0o755))

	assert.Equal(t, "shelf_core", python.ModuleName(os.DirFS(root), "libs/shelf"))
	assert.Equal(t, "flat", python.ModuleName(os.DirFS(root), "libs/flat"))
}
