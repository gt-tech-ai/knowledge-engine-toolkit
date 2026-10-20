package unit_test

import (
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
