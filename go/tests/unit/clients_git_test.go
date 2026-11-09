package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/git"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// exitWith is a failed command result with code and stderr.
func exitWith(code int, stderr string) interfaces.CmdResult {
	return interfaces.CmdResult{
		ExitCode: code, Stderr: []byte(stderr),
		Err:      apperr.New(apperr.CodeInternal, "exit status"),
		Stdout:   nil,
		Duration: 0,
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
	commandRunner := mocks.NewMockCommandRunner(gomock.NewController(t))
	gitClient, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec, Name: ""},
		git.Deps{Decorations: fixtures.Decorations(), Runner: commandRunner})
	require.NoError(t, err)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "ls-files", "-z", "--", textGo).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("a.go\x00b/c.go\x00")
		}))

	files, err := gitClient.LsFiles(ctx, textR, []string{textGo})
	require.NoError(t, err)
	assert.Equal(t, []string{textAGo, textBCGo}, files)

	commandRunner.EXPECT().RunBuffered(gomock.Any(), "/x", "git", "ls-files", "-z", "--").
		Return(exitWith(exitWithInput, "not a git repository"))

	files, err = gitClient.LsFiles(ctx, "/x", nil)
	require.NoError(t, err)
	assert.Empty(t, files)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "diff", "--quiet", textA, textB, "--", textX).
		Return(exitWith(1, ""))

	changed, err := gitClient.HasPathsChanged(ctx, textR, textA, textB, []string{textX})
	require.NoError(t, err)
	assert.True(t, changed)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "diff", "--cached", "--name-only", "-z",
			"--diff-filter=ACM", "--").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("s.go\x00")
		}))

	testClientsGitExecStagedAndFailures(ctx, t, commandRunner, gitClient)
}

// testClientsGitExecStagedAndFailures continues TestClientsGit_Exec: staged and
// failures.
func testClientsGitExecStagedAndFailures(
	ctx context.Context,
	t *testing.T,
	commandRunner *mocks.MockCommandRunner,
	gitClient interfaces.GitClient,
) {
	t.Helper()

	var err error

	staged, err := gitClient.StagedFiles(ctx, textR, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"s.go"}, staged)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "diff", "--name-only", "-z",
			"--merge-base", textMain).
		Return(exitWith(exitWithInputFailures, "fatal: bad revision"))

	_, err = gitClient.ChangedSince(ctx, textR, textMain)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
	assert.Contains(t, err.Error(), "fatal: bad revision")

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "rev-parse", "--verify", "--quiet", textNope).
		Return(exitWith(1, ""))

	_, err = gitClient.RevParse(ctx, textR, textNope)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "describe", "--tags", "--exact-match",
			"HEAD").
		Return(exitWith(exitWithInput, "no tag exactly matches"))

	tag, err := gitClient.Tag(ctx, textR)
	require.NoError(t, err)
	assert.Empty(t, tag)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "rev-parse", "--is-inside-work-tree").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("true\n")
		}))
	assert.True(t, gitClient.IsRepo(ctx, textR))

	require.NoError(t, gitClient.StageFiles(ctx, textR, nil))
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
	commandRunner := mocks.NewMockCommandRunner(gomock.NewController(t))
	gitClient, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec, Name: ""},
		git.Deps{Decorations: fixtures.Decorations(), Runner: commandRunner})
	require.NoError(t, err)

	probe := func(ref string) *gomock.Call {
		return commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textR, "git", "ls-remote", "--exit-code",
				"--heads", "--tags", textOrigin, ref)
	}

	probe(
		textV1,
	).Return(fixtures.Build(func(row *interfaces.CmdResult) {
		row.Stdout = []byte("abc\trefs/tags/v1\n")
	}))

	found, err := gitClient.RemoteHasRef(ctx, textR, textOrigin, textV1)
	require.NoError(t, err)
	assert.True(t, found)

	probe(textNope).Return(exitWith(exitWithInputRef, ""))

	found, err = gitClient.RemoteHasRef(ctx, textR, textOrigin, textNope)
	require.NoError(t, err)
	assert.False(t, found)

	probe("v2").Return(exitWith(exitWithInput, "fatal: could not read Username"))

	found, err = gitClient.RemoteHasRef(ctx, textR, textOrigin, "v2")
	assert.False(t, found)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	stub, err := git.NewFromConfig(
		ctx,
		stdx.Zero[git.Config](),
		git.Deps{Decorations: fixtures.Decorations(), Runner: nil},
	)
	require.NoError(t, err)

	found, err = stub.RemoteHasRef(ctx, textR, textOrigin, textV1)
	require.NoError(t, err)
	assert.False(t, found)
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
	gitClient, err := git.NewFromConfig(
		ctx,
		stdx.Zero[git.Config](),
		git.Deps{Decorations: fixtures.Decorations(), Runner: nil},
	)
	require.NoError(t, err)
	assert.False(t, gitClient.IsRepo(ctx, textR))

	files, err := gitClient.LsFiles(ctx, textR, nil)
	require.NoError(t, err)
	assert.Empty(t, files)

	_, err = git.NewFromConfig(
		ctx,
		git.Config{Kind: "jj", Name: ""},
		git.Deps{Decorations: fixtures.Decorations(), Runner: nil},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = git.NewFromConfig(ctx, git.Config{Kind: git.KindExec, Name: ""},
		git.Deps{Decorations: fixtures.Decorations(), Runner: nil})
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
	commandRunner := mocks.NewMockCommandRunner(gomock.NewController(t))
	gitClient, err := git.NewFromConfig(ctx, git.Config{Kind: git.KindExec, Name: ""},
		git.Deps{Decorations: fixtures.Decorations(), Runner: commandRunner})
	require.NoError(t, err)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "log", "--format=%H", "--", textAMd).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("abc\n")
		}))

	out, err := gitClient.Log(ctx, textR, "--format=%H", "--", textAMd)
	require.NoError(t, err)
	assert.Equal(t, "abc\n", string(out))

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", textStatus, "--porcelain", "--", textDocsLower).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(" M docs/a.md\n")
		}))

	out, err = gitClient.Status(ctx, textR, []string{textDocsLower})
	require.NoError(t, err)
	assert.Equal(t, " M docs/a.md\n", string(out))

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "ls-files", "-z", "--others",
			"--exclude-standard", "--", "*.md").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("new.md\x00d/n.md\x00")
		}))

	files, err := gitClient.LsUntracked(ctx, textR, []string{"*.md"})
	require.NoError(t, err)
	assert.Equal(t, []string{"new.md", "d/n.md"}, files)

	testClientsGitHistoryAndWorktreeDiffsAndWorktrees(ctx, t, commandRunner, gitClient)
}

// testClientsGitHistoryAndWorktreeDiffsAndWorktrees continues
// TestClientsGit_HistoryAndWorktree: diffs and worktrees.
func testClientsGitHistoryAndWorktreeDiffsAndWorktrees(
	ctx context.Context,
	t *testing.T,
	commandRunner *mocks.MockCommandRunner,
	gitClient interfaces.GitClient,
) {
	t.Helper()

	var files []string

	var err error

	grep := func(needle string) *gomock.Call {
		return commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textR, "git", "grep", "-l", "-z", "-I",
				"-F", textEKebab, needle)
	}
	grep(
		textDocsA,
	).Return(fixtures.Build(func(row *interfaces.CmdResult) {
		row.Stdout = []byte("x.md\x00y.go\x00")
	}))

	files, err = gitClient.Grep(ctx, textR, textDocsA)
	require.NoError(t, err)
	assert.Equal(t, []string{"x.md", "y.go"}, files)
	grep("nothing").Return(exitWith(1, ""))

	files, err = gitClient.Grep(ctx, textR, "nothing")
	require.NoError(t, err)
	assert.Empty(t, files)
	grep(textBroken).Return(exitWith(exitWithInput, "fatal: bad pattern"))

	testClientsGitHistoryAndWorktreeDiffsAndWorktreesMoveAndFailures(ctx,
		t,
		commandRunner,
		gitClient,
	)
}

// testClientsGitHistoryAndWorktreeDiffsAndWorktreesMoveAndFailures continues
// testClientsGitHistoryAndWorktreeDiffsAndWorktrees.
func testClientsGitHistoryAndWorktreeDiffsAndWorktreesMoveAndFailures(ctx context.Context,
	t *testing.T,
	commandRunner *mocks.MockCommandRunner,
	gitClient interfaces.GitClient,
) {
	t.Helper()

	_, err := gitClient.Grep(ctx, textR, textBroken)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", "mv", "--", textA, textB).
		Return(stdx.Zero[interfaces.CmdResult]())
	require.NoError(t, gitClient.Move(ctx, textR, textA, textB))
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textR, "git", textRm, "-q", "-f", "--", textA, textB).
		Return(stdx.Zero[interfaces.CmdResult]())
	require.NoError(t, gitClient.Remove(ctx, textR, []string{textA, textB}))
	require.NoError(t, gitClient.Remove(ctx, textR, nil))

	stub, err := git.NewFromConfig(ctx, stdx.Zero[git.Config](),
		git.Deps{Decorations: fixtures.Decorations(), Runner: nil})
	require.NoError(t, err)

	for _, read := range []func() (any, error){
		func() (any, error) { return stub.Log(ctx, textR) },
		func() (any, error) { return stub.Status(ctx, textR, nil) },
		func() (any, error) { return stub.LsUntracked(ctx, textR, nil) },
		func() (any, error) { return stub.Grep(ctx, textR, textX) },
	} {
		got, err := read()
		require.NoError(t, err)
		assert.Empty(t, got)
	}

	require.NoError(t, stub.Move(ctx, textR, textA, textB))
	require.NoError(t, stub.Remove(ctx, textR, []string{textA}))
}

const (
	// exitWithInput is the exit with input the tests use.
	exitWithInput = 128

	// exitWithInputFailures is the exit with input failures the tests use.
	exitWithInputFailures = 129

	// exitWithInputRef is the exit with input ref the tests use.
	exitWithInputRef = 2
)
