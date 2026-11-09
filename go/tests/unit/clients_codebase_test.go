package unit_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// writeFiles writes files under root.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), mkdirAllInputFiles))
		require.NoError(t, os.WriteFile(full, []byte(body), writeFileInput))
	}
}

// TestCodebaseClient_MemoryKindIsTheDefault tests the memory kind and the factory.
//
// Why this test is important:
//   - The memory kind is the stub every consumer's composition root boots on and every
//     check's unit test uses as its file fixture; an unknown kind must fail at boot.
//
// What it tests:
//   - An empty kind builds the memory codebase over Deps.Files; Text, Files and Changed
//     behave; a path climbing out of the repository is CodeInvalidInput; a missing file
//     is CodeNotFound; an unknown kind is CodeInvalidInput.
func TestCodebaseClient_MemoryKindIsTheDefault(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	codebase, err := clientcodebase.NewFromConfig(
		ctx,
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Files = map[string]string{"b.txt": "two\n", "a/a.txt": "one\n"}
		}),
	)
	require.NoError(t, err)

	files, err := codebase.Files(ctx, new(types.FileSelector))
	require.NoError(t, err)
	assert.Equal(t, []string{"a/a.txt", "b.txt"}, paths(files))

	text, err := codebase.Text(ctx, "a/a.txt")
	require.NoError(t, err)
	assert.Equal(t, "one\n", string(text.Bytes))

	changed, err := codebase.Changed(ctx, textMain)
	require.NoError(t, err)
	assert.Empty(t, changed)

	_, err = codebase.Text(ctx, "../etc/passwd")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = codebase.Text(ctx, "nope.txt")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	_, err = clientcodebase.NewFromConfig(
		ctx,
		clientcodebase.Config{Kind: "svn", Name: "", Root: ""},
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
		}),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestCodebaseClient_FSKind tests the plain-directory kind.
//
// Why this test is important:
//   - A check run outside a git checkout (a scratch tree, an unpacked archive) still
//     needs every file listed and read, and must never descend into a .git directory.
//
// What it tests:
//   - The fs kind lists every file under the root except .git, sorted by path (so
//     `a-b/q.md` precedes `a/p.md`, unlike a directory walk), reads contents, and
//     matches patterns over them.
func TestCodebaseClient_FSKind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"x/y.go": "package y // TODO-9\n", "z.md": "plain\n", ".git/HEAD": "ref\n",
		"a/p.md": "p\n", "a-b/q.md": "q\n",
	})

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		clientcodebase.Config{Kind: clientcodebase.KindFS, Root: root, Name: ""},
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
		}),
	)
	require.NoError(t, err)

	files, err := codebase.Files(t.Context(), new(types.FileSelector))
	require.NoError(t, err)
	assert.Equal(t, []string{"a-b/q.md", "a/p.md", "x/y.go", "z.md"}, paths(files))

	hits, err := codebase.Match(t.Context(), new(types.FileSelector),
		[]*regexp.Regexp{regexp.MustCompile(`TODO-\d`)})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "x/y.go", hits[0].File)
}

// TestCodebaseClient_GitKind tests the git kind over a mocked command runner.
//
// Why this test is important:
//   - The git kind is what a real run uses: its file list must be git's tracked and
//     untracked-but-not-ignored files (never a build artifact under an ignored dir), it
//     must survive a deleted-but-tracked file, a directory that is not a repository and
//     an unknown base.
//
// What it tests:
//   - Files unions `ls-files --cached` and `ls-files --others --exclude-standard`,
//     marks the untracked file, and drops a tracked path missing on disk; exit 128
//     yields no files; Changed unions the merge-base diff with the untracked files; an
//     unresolvable base is CodeNotFound.
func TestCodebaseClient_GitKind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFiles(t, root, map[string]string{textAGo: textPackageA, textNewTxt: "n\n"})

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--cached").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("a.go\x00gone.go\x00")
		})).
		AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--exclude-standard").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("new.txt\x00")
		})).
		AnyTimes()
	runner.EXPECT().
		RunBuffered(gomock.Any(), root, "git", "rev-parse", "--verify", "--quiet",
			"main^{commit}").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("abc\n")
		}))
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "diff", "--name-only", "-z",
		"--merge-base", textMain).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("a.go\x00")
		}))
	runner.EXPECT().
		RunBuffered(gomock.Any(), root, "git", "rev-parse", "--verify", "--quiet",
			"nope^{commit}").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Err = apperr.New(apperr.CodeInternal, "exit 1")
			row.ExitCode = 1
		}))

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		clientcodebase.Config{Kind: clientcodebase.KindGit, Root: root, Name: ""},
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Runner = runner
		}),
	)

	testCodebaseClientGitKindChecksFiles(t, root, codebase, err)
}

// testCodebaseClientGitKindChecksFiles continues TestCodebaseClient_GitKind.
func testCodebaseClientGitKindChecksFiles(
	t *testing.T,
	root string,
	codebase interfaces.Codebase,
	err error,
) {
	t.Helper()

	require.NoError(t, err)

	files, err := codebase.Files(t.Context(), new(types.FileSelector))
	require.NoError(t, err)
	assert.Equal(
		t,
		[]types.File{
			{Path: textAGo, Untracked: false, Ignored: false},
			{Path: textNewTxt, Untracked: true, Ignored: false},
		},
		files,
	)

	changed, err := codebase.Changed(t.Context(), textMain)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{textAGo: true, textNewTxt: true}, changed)

	_, err = codebase.Changed(t.Context(), textNope)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	notRepo := mocks.NewMockCommandRunner(gomock.NewController(t))
	notRepo.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "git", gomock.Any()).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Err = apperr.New(apperr.CodeInternal, "128")
			row.ExitCode = 128
		})).AnyTimes()

	codebase, err = clientcodebase.NewFromConfig(
		t.Context(),
		clientcodebase.Config{Kind: clientcodebase.KindGit, Root: root, Name: ""},
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Runner = notRepo
		}),
	)
	require.NoError(t, err)

	files, err = codebase.Files(t.Context(), new(types.FileSelector))
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestCodebaseClient_RunsThroughTheClientTier tests that the factory decorates the
// codebase.
//
// Why this test is important:
//   - A codebase's reads are I/O like any client's: each must be observable and tunable
//     (timeouts, metrics) under its own operation name from the decoration config.
//
// What it tests:
//   - A Files call on a factory-built codebase is counted as client operation "files".
func TestCodebaseClient_RunsThroughTheClientTier(t *testing.T) {
	t.Parallel()

	reg := promclient.NewRegistry()
	decs := decorate.Fixed(fixtures.BuildRef(func(row *decorate.Deps) {
		row.Metrics = prom.NewFromRegistry(reg)
	}))
	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = decs
			row.Files = map[string]string{textA: textB}
		}),
	)
	require.NoError(t, err)

	_, err = codebase.Files(t.Context(), new(types.FileSelector))
	require.NoError(t, err)
	assert.InDelta(t, wantClientOpCounter, clientOpCounter(t, reg, textFiles), 0)
}

const (
	// mkdirAllInputFiles is the mkdir all input files the tests use.
	mkdirAllInputFiles = 0o750
)

const (
	// textNewTxt is the literal "new.txt".
	textNewTxt = "new.txt"
)
