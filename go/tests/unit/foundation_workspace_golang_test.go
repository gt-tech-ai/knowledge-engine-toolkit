package unit_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/workspace/golang"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// wsWrite writes content to path, creating parent directories.
func wsWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// wsGoMod seeds <dir>/go.mod so module discovery finds a module root there.
func wsGoMod(t *testing.T, dir string) {
	t.Helper()
	wsWrite(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.26\n")
}

// goListOut answers a mocked `go list` with the given packages (none: empty output).
func goListOut(pkgs ...string) interfaces.CmdResult {
	var out strings.Builder
	for _, p := range pkgs {
		out.WriteString(p + "\n")
	}
	return interfaces.CmdResult{Stdout: []byte(out.String())}
}

// TestGoWorkModules tests reading the go.work use directives into module paths.
//
// Why this test is important:
//   - Every test/lint/format fan-out enumerates modules from go.work; a parser that
//     kept ".", ignored the filter, missed a one-line use, or read a comment as a module
//     would run the wrong set of modules.
//
// What it tests:
//   - Block and one-line use directives come back relative to root, in file order,
//     with "." and comments excluded; the filter drops modules by relative path.
//   - A missing go.work is CodeNotFound; a malformed one is CodeInvalidInput.
func TestGoWorkModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(
		t,
		filepath.Join(root, "go.work"),
		"go 1.26.5\n\nuse (\n\t.\n\t// the core library\n\t./libs/core\n"+
			"\t./services/catalog\n)\n\nuse ./tools/gen\n",
	)

	mods, err := golang.GoWorkModules(os.DirFS(root), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"libs/core", "services/catalog", "tools/gen"}, mods)

	filtered, err := golang.GoWorkModules(os.DirFS(root), func(rel string) bool {
		return !strings.HasPrefix(rel, "services/")
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"libs/core", "tools/gen"}, filtered)

	_, err = golang.GoWorkModules(os.DirFS(t.TempDir()), nil)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), err)

	bad := t.TempDir()
	wsWrite(t, filepath.Join(bad, "go.work"), "go 1.26.5\nuse (\n")
	_, err = golang.GoWorkModules(os.DirFS(bad), nil)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)
}

// TestScopedWork tests the go.work a scoped run builds a subset of the modules with.
//
// Why this test is important:
//   - A scoped run builds against the scoped go.work alone. If it dropped the root
//     go.work's go version, replace, toolchain or godebug directives, it would resolve a
//     different dependency (a published module instead of a local replace) or toolchain
//     than every other command; a relative replace target would break once the file is
//     written outside the repository.
//
// What it tests:
//   - go, toolchain and godebug (line and block) are kept; replace directives (line and
//     block) are kept with local-path targets made absolute against root and
//     module-version targets as written; only the requested modules are used, as
//     absolute paths.
//   - A go.work with no go directive is CodeInvalidInput; a missing one CodeNotFound.
func TestScopedWork(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(t, filepath.Join(root, "go.work"), `go 1.26.5

toolchain go1.26.6

godebug default=go1.21

godebug (
	panicnil=1
)

use (
	.
	./a
	./b
)

// the local engine
replace example.com/engine => ../engine

replace (
	example.com/y v1.0.0 => example.com/y v1.1.0
	example.com/z => ./z
)
`)

	data, err := golang.ScopedWork(os.DirFS(root), root, "a")
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "go 1.26.5\n")
	assert.Contains(t, content, "toolchain go1.26.6\n")
	assert.Contains(t, content, "default=go1.21")
	assert.Contains(t, content, "panicnil=1")
	assert.Contains(
		t,
		content,
		"example.com/engine => "+filepath.Join(root, "..", "engine"),
	)
	assert.Contains(t, content, "example.com/y v1.0.0 => example.com/y v1.1.0")
	assert.Contains(t, content, "example.com/z => "+filepath.Join(root, "z"))
	assert.Contains(t, content, filepath.Join(root, "a"))
	assert.NotContains(t, content, filepath.Join(root, "b"), "only the requested modules")

	bad := t.TempDir()
	wsWrite(t, filepath.Join(bad, "go.work"), "use (\n\t.\n)\n")
	_, err = golang.ScopedWork(os.DirFS(bad), bad, "a")
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)

	missing := t.TempDir()
	_, err = golang.ScopedWork(os.DirFS(missing), missing, "a")
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), err)
}

// TestModulePath tests reading the root module path from go.mod.
//
// Why this test is important:
//   - The module path prefixes generated import paths; an empty or wrong one would
//     produce code that does not compile.
//
// What it tests:
//   - A go.mod's module directive is returned without the keyword.
//   - A missing go.mod, or one with no module directive, is CodeNotFound.
func TestModulePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(
		t,
		filepath.Join(root, "go.mod"),
		"module github.com/example/shelf\n\ngo 1.26\n",
	)
	got, err := golang.ModulePath(os.DirFS(root))
	require.NoError(t, err)
	assert.Equal(t, "github.com/example/shelf", got)

	_, err = golang.ModulePath(os.DirFS(t.TempDir()))
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), err)

	noModule := t.TempDir()
	wsWrite(t, filepath.Join(noModule, "go.mod"), "go 1.26\n")
	_, err = golang.ModulePath(os.DirFS(noModule))
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), err)
}

// TestHasGeneratedPackages tests the generated-protobuf presence probe.
//
// Why this test is important:
//   - A stale go.mod with no generated *.pb.go files is a broken state that callers
//     must regenerate; a false positive makes downstream `go` commands fail, a false
//     negative regenerates for nothing.
//
// What it tests:
//   - A nested *.pb.go is found; an empty or missing directory reports false.
func TestHasGeneratedPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "gen", "pb"), 0o755))
	assert.False(t, golang.HasGeneratedPackages(os.DirFS(root), "gen/pb"), "empty dir")
	assert.False(t, golang.HasGeneratedPackages(os.DirFS(root), "nope"), "missing dir")

	wsWrite(
		t,
		filepath.Join(root, "gen", "pb", "books", "v1", "books.pb.go"),
		"package v1\n",
	)
	assert.True(t, golang.HasGeneratedPackages(os.DirFS(root), "gen/pb"))
}

// TestStagedModules tests resolving staged files to the modules a tidy hook runs in.
//
// Why this test is important:
//   - go mod tidy must run once in each module a commit touches; a wrong directory
//     tidies the wrong go.mod, a duplicate wastes a run, and a machine-managed module
//     the caller excludes must never be tidied.
//
// What it tests:
//   - A staged file resolves to its nearest module; files in one module dedup to one
//     entry; two modules yield both; a file under no sub-module falls back to ".".
//   - keep drops a module by relative path; no staged files is nil.
func TestStagedModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsGoMod(t, root)
	wsGoMod(t, filepath.Join(root, "libs", "alpha"))
	wsGoMod(t, filepath.Join(root, "libs", "beta"))
	wsGoMod(t, filepath.Join(root, "gen", "pb"))
	fsys := os.DirFS(root)

	got := golang.StagedModules(fsys, []string{
		"libs/alpha/a.go", "libs/alpha/sub/b.go", "libs/beta/c.go", "cmd/main.go",
	}, nil)
	assert.Equal(t, []string{"libs/alpha", "libs/beta", "."}, got)

	kept := golang.StagedModules(fsys, []string{"gen/pb/x.pb.go", "libs/beta/c.go"},
		func(rel string) bool { return !strings.HasPrefix(rel, "gen/") })
	assert.Equal(t, []string{"libs/beta"}, kept)

	assert.Nil(t, golang.StagedModules(fsys, nil, nil))
}

// TestIsRaceSupported tests the race-detector capability predicate.
//
// Why this test is important:
//   - A test runner toggles -race on it; wrong on windows/arm64 (the one unsupported
//     target), the test job would fail to start.
//
// What it tests:
//   - IsRaceSupported is false exactly on windows/arm64.
func TestIsRaceSupported(t *testing.T) {
	t.Parallel()
	winArm := runtime.GOOS == "windows" && runtime.GOARCH == "arm64"
	assert.Equal(t, !winArm, golang.IsRaceSupported())
}

// TestLister_GoListPackagesMemoizes tests that GoListPackages runs `go list` at most
// once per directory, concurrent first calls included, and caches failures too.
//
// Why this test is important:
//   - Every phase of a run probes the same modules; without the memo each phase reruns
//     `go list ./...` per module. The cache must survive concurrent probes and remember
//     failures so an unlistable dir is not retried every phase.
//
// What it tests (mocked runner — Times(1) fails the test on a second run):
//   - 20 concurrent first calls plus a later call for one dir run `go list` once and
//     all return the same packages.
//   - A failed listing runs once; both calls return the same error.
func TestLister_GoListPackagesMemoizes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	okDir := filepath.Join(t.TempDir(), "ok")
	badDir := filepath.Join(t.TempDir(), "bad")
	lister := golang.NewLister(nil, "")

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().RunBuffered(gomock.Any(), okDir, "go", "list", "./...").
		Return(goListOut("example.com/ok", "example.com/ok/sub")).Times(1)
	runner.EXPECT().RunBuffered(gomock.Any(), badDir, "go", "list", "./...").
		Return(interfaces.CmdResult{
			Err:    apperr.New(apperr.CodeInternal, "exit status 1"),
			Stderr: []byte("no go.mod"),
		}).Times(1)

	const goroutines = 20
	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			pkgs, err := lister.GoListPackages(ctx, runner, okDir)
			assert.NoError(t, err)
			results[i] = pkgs
		})
	}
	wg.Wait()
	later, err := lister.GoListPackages(ctx, runner, okDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/ok", "example.com/ok/sub"}, later)
	for i, r := range results {
		assert.Equal(t, later, r, "concurrent call %d saw a different result", i)
	}

	_, errFirst := lister.GoListPackages(ctx, runner, badDir)
	require.Error(t, errFirst)
	_, errSecond := lister.GoListPackages(ctx, runner, badDir)
	require.Error(t, errSecond)
	assert.Equal(t, errFirst.Error(), errSecond.Error(), "the cached error is returned")
}

// TestLister_FilterModulesWithGoPackages tests which modules survive the
// loadable-package probe.
//
// Why this test is important:
//   - Test/lint/vet/format runs discover modules through this filter. Keeping a
//     package-less dir makes `go test`/`go vet` error; dropping a real module whose
//     `go list` fails would let a package that fails to compile pass unseen.
//
// What it tests (mocked runner — no subprocess):
//   - A module with packages is kept; one with an empty listing is dropped.
//   - A real module (has go.mod) whose `go list` fails is kept; a non-module dir whose
//     `go list` fails is dropped. Survivors keep their input order.
func TestLister_FilterModulesWithGoPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, mod := range []string{"a", "empty", "notmod", "b"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, mod), 0o755))
	}
	wsGoMod(t, filepath.Join(root, "broken"))
	failed := interfaces.CmdResult{
		Err:    apperr.New(apperr.CodeInternal, "exit status 1"),
		Stderr: []byte("conflict"),
	}

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	for mod, res := range map[string]interfaces.CmdResult{
		"a":      goListOut("example.com/a"),
		"empty":  goListOut(),
		"broken": failed,
		"notmod": failed,
		"b":      goListOut("example.com/b", "example.com/b/x"),
	} {
		runner.EXPECT().
			RunBuffered(gomock.Any(), filepath.Join(root, mod), "go", "list", "./...").
			Return(res)
	}

	got := golang.NewLister(os.DirFS(root), root).FilterModulesWithGoPackages(
		t.Context(), runner, root, []string{"a", "empty", "broken", "notmod", "b"},
	)
	assert.Equal(t, []string{"a", "broken", "b"}, got)
}

// TestLister_HasGoPackagesWithTags tests the build-tag package probe.
//
// Why this test is important:
//   - It decides whether a module has tag-guarded packages to run. A false negative
//     silently skips them; a real module that fails to load under the tags must be
//     reported so its build error surfaces.
//
// What it tests (mocked runner — no subprocess):
//   - `go list -tags=<tags> ./...` is what runs; packages → true, empty → false.
//   - A load failure inside a real module → true; inside a non-module dir → false.
func TestLister_HasGoPackagesWithTags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	module, notModule := filepath.Join(root, "m"), filepath.Join(root, "n")
	require.NoError(t, os.MkdirAll(notModule, 0o750))
	wsGoMod(t, module)
	failed := interfaces.CmdResult{Err: apperr.New(apperr.CodeInternal, "exit status 1")}

	cases := []struct {
		name string
		dir  string
		res  interfaces.CmdResult
		want bool
	}{
		{"packages", module, goListOut("m/tests/integration"), true},
		{"no packages", module, goListOut(), false},
		{"load failure in a module", module, failed, true},
		{"failure outside a module", notModule, failed, false},
	}
	for _, tc := range cases {
		runner := mocks.NewMockCommandRunner(gomock.NewController(t))
		runner.EXPECT().
			RunBuffered(gomock.Any(), tc.dir, "go", "list", "-tags=integration", "./...").
			Return(tc.res)
		assert.Equal(t, tc.want, golang.NewLister(os.DirFS(root), root).
			HasGoPackagesWithTags(t.Context(), runner, tc.dir, "integration"), tc.name)
	}
}
