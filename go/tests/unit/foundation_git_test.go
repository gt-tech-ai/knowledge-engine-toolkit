package unit_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/git"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// --- StagedFiles ---

// TestStagedFiles_ReturnsParsedPaths tests that git's newline-delimited
// --name-only output is split into individual file paths.
//
// Why this test is important:
//   - fmt/tidy/gen --staged operate on exactly the files StagedFiles returns;
//     a parsing slip would format the wrong files or silently skip staged ones.
//
// What it tests:
//   - Two newline-separated paths from git become a two-element []string in order.
func TestStagedFiles_ReturnsParsedPaths(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "git", gomock.Any()).
		Return(interfaces.CmdResult{Stdout: []byte("pkg/foo.go\npkg/bar.go\n")})

	got, err := git.StagedFiles(context.Background(), r, "/repo")
	require.NoError(t, err)
	want := []string{"pkg/foo.go", "pkg/bar.go"}
	require.Equal(t, want, got)
}

// TestStagedFiles_EmptyOutputReturnsNil tests that no staged files yields nil
// rather than a slice containing one empty string.
//
// Why this test is important:
//   - A blank line from git must not be treated as a real path; callers range
//     over the result and would otherwise act on an empty-named file.
//
// What it tests:
//   - Empty git stdout produces a nil slice, not [""].
func TestStagedFiles_EmptyOutputReturnsNil(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "git", gomock.Any()).
		Return(interfaces.CmdResult{Stdout: []byte("")})

	got, err := git.StagedFiles(context.Background(), r, "/repo")
	require.NoError(t, err)
	assert.Nil(t, got, "expected nil")
}

// --- StageFiles ---

// TestStageFiles_CallsGitAdd tests that StageFiles invokes `git add -- <paths>`
// with the pathspec terminator and the given paths in order.
//
// Why this test is important:
//   - The `--` terminator stops git from interpreting a path that looks like a
//     flag; omitting it could let a crafted filename inject a git option.
//
// What it tests:
//   - StageFiles forwards exactly [add, --, a.go, b.go] to the runner.
func TestStageFiles_CallsGitAdd(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	// The exact-args expectation is the assertion: gomock verifies StageFiles
	// forwards `git add -- a.go b.go` and nothing else.
	r.EXPECT().
		Run(gomock.Any(), "/repo", "git", "add", "--", "a.go", "b.go").
		Return(nil)

	require.NoError(t, git.StageFiles(context.Background(), r, "/repo", "a.go", "b.go"))
}

// TestStageFiles_NoPaths_NoOp tests that StageFiles with no paths runs no git
// command at all.
//
// Why this test is important:
//   - `git add --` with no pathspec would stage nothing or error; short-circuiting
//     avoids spawning a pointless subprocess on the empty-changeset path.
//
// What it tests:
//   - Calling StageFiles with zero paths leaves the runner untouched (no Run call).
func TestStageFiles_NoPaths_NoOp(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	// No EXPECT() on Run: gomock fails the test if StageFiles issues any command
	// for an empty path set, which is the "no run call" assertion.
	r := mocks.NewMockCommandRunner(ctrl)

	require.NoError(t, git.StageFiles(context.Background(), r, "/repo"))
}

// --- HasPathsChanged ---

// TestHasPathsChanged_ExitOneMeansChanged tests that a non-zero exit from
// `git diff --quiet` is interpreted as "paths changed", not as an error.
//
// Why this test is important:
//   - gen --if-changed keys off this result; misreading the conventional exit-1
//     signal as a failure would abort code generation when a regen was needed.
//
// What it tests:
//   - A git ExitError surfaces as changed=true with a nil error return.
func TestHasPathsChanged_ExitOneMeansChanged(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	// Simulate git diff --quiet returning exit code 1 (differences found).
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "git", gomock.Any()).
		Return(interfaces.CmdResult{Err: &exec.ExitError{}})

	changed, err := git.HasPathsChanged(
		context.Background(),
		r,
		"/repo",
		"HEAD~1",
		"HEAD",
		"pkg/",
	)
	require.NoError(t, err)
	assert.True(t, changed, "expected changed=true when git exits non-zero")
}

// TestHasPathsChanged_NoErrorMeansUnchanged tests that a clean (exit-zero) git
// diff is interpreted as "no changes".
//
// Why this test is important:
//   - This is the no-op path that lets --if-changed skip regeneration; a false
//     "changed" here would force needless work on every run.
//
// What it tests:
//   - A nil runner error yields changed=false with a nil error return.
func TestHasPathsChanged_NoErrorMeansUnchanged(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "git", gomock.Any()).
		Return(interfaces.CmdResult{})

	changed, err := git.HasPathsChanged(
		context.Background(),
		r,
		"/repo",
		"HEAD~1",
		"HEAD",
	)
	require.NoError(t, err)
	assert.False(t, changed, "expected changed=false when git exits zero")
}
