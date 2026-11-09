package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/git"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// exitWith is a failed command result with code and stderr.
func exitWith(code int, stderr string) interfaces.CmdResult {
	return interfaces.CmdResult{
		ExitCode: code, Stderr: []byte(stderr),
		Err: apperr.New(apperr.CodeInternal, "exit status"),
	}
}

// TestClientsGit_Exec tests the git binary backend over a mocked runner.
//
// Why this test is important:
//   - Every CLI path that asks git a question (changed files, staged files, the
//     current tag) goes through this client; a misparsed listing or a misread exit
//     code makes a gate check the wrong files or fail outside a repository.
//
// What it tests:
//   - Listings parse NUL-delimited output; ls-files outside a repository is empty;
//     diff --quiet exit 1 means changed; an unknown revision is NOT_FOUND; an untagged
//     HEAD has no tag; a failing command is UPSTREAM carrying its stderr; IsRepo reads
//     rev-parse; StageFiles with no paths runs nothing.
func TestClientsGit_Exec(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec},
		git.Deps{Decorations: fixtures.Decorations(), Runner: r})
	require.NoError(t, err)

	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "ls-files", "-z", "--", "*.go").
		Return(interfaces.CmdResult{Stdout: []byte("a.go\x00b/c.go\x00")})

	files, err := c.LsFiles(ctx, "/r", []string{"*.go"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.go", "b/c.go"}, files)

	r.EXPECT().RunBuffered(gomock.Any(), "/x", "git", "ls-files", "-z", "--").
		Return(exitWith(128, "not a git repository"))

	files, err = c.LsFiles(ctx, "/x", nil)
	require.NoError(t, err)
	assert.Empty(t, files)

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "diff", "--quiet", "a", "b", "--", "x").
		Return(exitWith(1, ""))

	changed, err := c.HasPathsChanged(ctx, "/r", "a", "b", []string{"x"})
	require.NoError(t, err)
	assert.True(t, changed)

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "diff", "--cached", "--name-only", "-z",
			"--diff-filter=ACM", "--").
		Return(interfaces.CmdResult{Stdout: []byte("s.go\x00")})

	testClientsGitExecRest(t, ctx, r, c)
}

// testClientsGitExecRest continues TestClientsGit_Exec with its remaining steps.
func testClientsGitExecRest(
	t *testing.T,
	ctx context.Context,
	r *mocks.MockCommandRunner,
	c interfaces.GitClient,
) {
	t.Helper()

	var err error

	staged, err := c.StagedFiles(ctx, "/r", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"s.go"}, staged)

	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "diff", "--name-only", "-z",
		"--merge-base", "main").Return(exitWith(129, "fatal: bad revision"))

	_, err = c.ChangedSince(ctx, "/r", "main")
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
	assert.Contains(t, err.Error(), "fatal: bad revision")

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "rev-parse", "--verify", "--quiet", "nope").
		Return(exitWith(1, ""))

	_, err = c.RevParse(ctx, "/r", "nope")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "describe", "--tags", "--exact-match",
			"HEAD").
		Return(exitWith(128, "no tag exactly matches"))

	tag, err := c.Tag(ctx, "/r")
	require.NoError(t, err)
	assert.Empty(t, tag)

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "rev-parse", "--is-inside-work-tree").
		Return(interfaces.CmdResult{Stdout: []byte("true\n")})
	assert.True(t, c.IsRepo(ctx, "/r"))

	require.NoError(t, c.StageFiles(ctx, "/r", nil))
}

// TestClientsGit_RemoteHasRef tests the remote ref probe over a mocked runner.
//
// Why this test is important:
//   - a release pin is checked against the remote before a deploy syncs it; a missing
//     ref and a remote that cannot be asked (no credentials, no network) must be told
//     apart, or an outage reads as a typo'd release and a typo as an outage.
//
// What it tests:
//   - `git ls-remote --exit-code --heads --tags <remote> <ref>` exiting 0 is true; exit 2
//     (no matching ref) is false with no error; any other failure is CodeUnavailable;
//     the stub reports false.
func TestClientsGit_RemoteHasRef(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec},
		git.Deps{Decorations: fixtures.Decorations(), Runner: r})
	require.NoError(t, err)

	probe := func(ref string) *gomock.Call {
		return r.EXPECT().
			RunBuffered(gomock.Any(), "/r", "git", "ls-remote", "--exit-code",
				"--heads", "--tags", "origin", ref)
	}

	probe("v1").Return(interfaces.CmdResult{Stdout: []byte("abc\trefs/tags/v1\n")})

	ok, err := c.RemoteHasRef(ctx, "/r", "origin", "v1")
	require.NoError(t, err)
	assert.True(t, ok)

	probe("nope").Return(exitWith(2, ""))

	ok, err = c.RemoteHasRef(ctx, "/r", "origin", "nope")
	require.NoError(t, err)
	assert.False(t, ok)

	probe("v2").Return(exitWith(128, "fatal: could not read Username"))

	ok, err = c.RemoteHasRef(ctx, "/r", "origin", "v2")
	assert.False(t, ok)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	stub, err := git.NewFromConfig(
		ctx,
		git.Config{},
		git.Deps{Decorations: fixtures.Decorations()},
	)
	require.NoError(t, err)

	ok, err = stub.RemoteHasRef(ctx, "/r", "origin", "v1")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestClientsGit_StubAndFactory tests the default kind and the factory's failures.
//
// Why this test is important:
//   - The stub is what a composition root boots on with no git; a typo in the kind or
//     a missing runner must fail at boot.
//
// What it tests:
//   - The empty kind is the stub (no repository, no files); an unknown kind and exec
//     without a runner are INVALID_INPUT.
func TestClientsGit_StubAndFactory(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	c, err := git.NewFromConfig(
		ctx,
		git.Config{},
		git.Deps{Decorations: fixtures.Decorations()},
	)
	require.NoError(t, err)
	assert.False(t, c.IsRepo(ctx, "/r"))

	files, err := c.LsFiles(ctx, "/r", nil)
	require.NoError(t, err)
	assert.Empty(t, files)

	_, err = git.NewFromConfig(
		ctx,
		git.Config{Kind: "jj"},
		git.Deps{Decorations: fixtures.Decorations()},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = git.NewFromConfig(ctx, git.Config{Kind: git.KindExec},
		git.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestClientsGit_HistoryAndWorktree tests the history, status, search and tree-edit
// operations over a mocked runner.
//
// Why this test is important:
//   - A step that reads history or edits the tree must reach git through the client, not
//     a raw runner, so the client tier decorates it; a misread exit code would make a
//     search with no match a failure or a failed grep look like an empty result.
//
// What it tests:
//   - Log and Status return git's output; LsUntracked parses NUL-delimited untracked,
//     non-ignored files; Grep lists matching files, exit 1 is none and any other failure
//     is UPSTREAM; Move runs `git mv --`; Remove runs `git rm -q -f --` and does nothing
//     with no paths; the stub returns nothing and touches nothing.
func TestClientsGit_HistoryAndWorktree(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec},
		git.Deps{Decorations: fixtures.Decorations(), Runner: r})
	require.NoError(t, err)

	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "log", "--format=%H", "--", "a.md").
		Return(interfaces.CmdResult{Stdout: []byte("abc\n")})

	out, err := c.Log(ctx, "/r", "--format=%H", "--", "a.md")
	require.NoError(t, err)
	assert.Equal(t, "abc\n", string(out))

	r.EXPECT().
		RunBuffered(gomock.Any(), "/r", "git", "status", "--porcelain", "--", "docs").
		Return(interfaces.CmdResult{Stdout: []byte(" M docs/a.md\n")})

	out, err = c.Status(ctx, "/r", []string{"docs"})
	require.NoError(t, err)
	assert.Equal(t, " M docs/a.md\n", string(out))

	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "ls-files", "-z", "--others",
		"--exclude-standard", "--", "*.md").
		Return(interfaces.CmdResult{Stdout: []byte("new.md\x00d/n.md\x00")})

	files, err := c.LsUntracked(ctx, "/r", []string{"*.md"})
	require.NoError(t, err)
	assert.Equal(t, []string{"new.md", "d/n.md"}, files)

	testClientsGitHistoryAndWorktreeRest(t, ctx, r, c)
}

// testClientsGitHistoryAndWorktreeRest continues TestClientsGit_HistoryAndWorktree with
// its remaining steps.
func testClientsGitHistoryAndWorktreeRest(
	t *testing.T,
	ctx context.Context,
	r *mocks.MockCommandRunner,
	c interfaces.GitClient,
) {
	t.Helper()

	var files []string

	var err error

	grep := func(needle string) *gomock.Call {
		return r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "grep", "-l", "-z", "-I",
			"-F", "-e", needle)
	}
	grep("docs/a").Return(interfaces.CmdResult{Stdout: []byte("x.md\x00y.go\x00")})

	files, err = c.Grep(ctx, "/r", "docs/a")
	require.NoError(t, err)
	assert.Equal(t, []string{"x.md", "y.go"}, files)
	grep("nothing").Return(exitWith(1, ""))

	files, err = c.Grep(ctx, "/r", "nothing")
	require.NoError(t, err)
	assert.Empty(t, files)
	grep("broken").Return(exitWith(128, "fatal: bad pattern"))

	_, err = c.Grep(ctx, "/r", "broken")
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))

	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "mv", "--", "a", "b").
		Return(interfaces.CmdResult{})
	require.NoError(t, c.Move(ctx, "/r", "a", "b"))
	r.EXPECT().RunBuffered(gomock.Any(), "/r", "git", "rm", "-q", "-f", "--", "a", "b").
		Return(interfaces.CmdResult{})
	require.NoError(t, c.Remove(ctx, "/r", []string{"a", "b"}))
	require.NoError(t, c.Remove(ctx, "/r", nil))

	stub, err := git.NewFromConfig(ctx, git.Config{},
		git.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)

	for _, read := range []func() (any, error){
		func() (any, error) { return stub.Log(ctx, "/r") },
		func() (any, error) { return stub.Status(ctx, "/r", nil) },
		func() (any, error) { return stub.LsUntracked(ctx, "/r", nil) },
		func() (any, error) { return stub.Grep(ctx, "/r", "x") },
	} {
		got, err := read()
		require.NoError(t, err)
		assert.Empty(t, got)
	}

	require.NoError(t, stub.Move(ctx, "/r", "a", "b"))
	require.NoError(t, stub.Remove(ctx, "/r", []string{"a"}))
}
