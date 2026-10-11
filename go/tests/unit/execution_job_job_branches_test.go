package unit_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestJob_MissingToolSkips tests that a tool-backed job whose required binary is
// absent (RequireTool errors) yields a single Skip result and no error, rather
// than failing the gate or attempting to run the command.
//
// Why this test is important:
//   - Tool-backed jobs are optional: a missing binary (e.g. an uninstalled
//     linter) must degrade to Skip so the gate is honest about what ran without
//     blocking the whole run on a machine that lacks the tool. If a missing tool
//     surfaced as a Fail, developers without every optional tool installed could
//     never get a green gate; if it silently proceeded, the command would run with
//     a nonexistent binary.
//
// What it tests:
//   - With a runner whose RequireTool returns an error, Execute returns exactly
//     one StatusSkip result carrying the tool error and never invokes the
//     discoverer or the command runner.
func TestJob_MissingToolSkips(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	// RequireTool fails => the whole job is a skip; Run is never expected, so any
	// attempt to run the command would surface as an unexpected-call failure.
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().RequireTool("go", gomock.Any()).
		Return(apperr.Sentinel("go: command not found")).
		Times(1)

	// The discoverer must never run once the tool is missing; a panic here would
	// prove the skip short-circuited too late.
	disc := interfaces.DiscovererFunc[string](
		func(context.Context, string) ([]string, error) {
			t.Error("discoverer must not run when the required tool is missing")
			return nil, nil
		},
	)

	j := job.New[string]("build", "ci").
		WithTool(goToolSpec(t)).
		WithDiscoverer(disc).
		WithMapper(workUnit).
		Build()

	results, err := j.Execute(context.Background(), r, "/repo")
	require.NoError(t, err, "a missing tool is a skip, not an error")
	require.Len(t, results, 1)
	assert.Equal(t, types.StatusSkip, results[0].Status)
	assert.Contains(
		t,
		results[0].Error,
		"command not found",
		"skip result should carry the tool error",
	)
}

// TestSingle_RelativeDirJoinsRoot tests that a Single job with a relative working
// directory resolves it against the repo root before running the command.
//
// Why this test is important:
//   - Most Single-backed jobs (lint/format/typecheck) describe their working
//     directory relative to the repo root; the command must run in the correct
//     absolute directory or it operates on the wrong tree (or fails outright). An
//     absolute dir is passed through, but a relative one must be joined onto root
//     — this pins that join so a relative-dir job isn't silently run from the
//     process CWD.
//
// What it tests:
//   - A Single job whose dirFn returns a relative path runs the command with
//     Dir = filepath.Join(root, relDir).
func TestSingle_RelativeDirJoinsRoot(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	var gotDir string
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	r.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, dir, _ string, _ ...string) error {
			gotDir = dir
			return nil
		},
	).Times(1)

	j := job.Single(
		"build", "ci", goToolSpec(t),
		func(_ string) []string { return []string{"build", "./..."} },
		func(_ string) string { return "sub/dir" }, // relative — must be joined onto root
	)

	_, err := j.Execute(context.Background(), r, "/repo")
	require.NoError(t, err)
	assert.Equal(
		t,
		filepath.FromSlash("/repo/sub/dir"),
		gotDir,
		"relative dir must be resolved against root",
	)
}

// TestJob_RetryAbortsOnContextCancelDuringDelay tests that a cancelled context
// stops a retry loop during its inter-attempt delay instead of sleeping out the
// backoff and running another doomed attempt.
//
// Why this test is important:
//   - On Ctrl+C or a deadline, a job mid-backoff must abandon promptly; sleeping
//     through the delay and firing another attempt wastes the user's time and can
//     keep hitting a failing dependency after the run was told to stop. This pins
//     that the delay honours cancellation — the retry runs the command exactly
//     once (the pre-delay attempt) and then bails on ctx.Done.
//
// What it tests:
//   - With WithRetry(1) and a long WithRetryDelay, a runner that cancels the
//     context on its first (failing) call makes Execute return a single Fail
//     result and invoke the command exactly once (the delay's ctx.Done wins before
//     the second attempt).
func TestJob_RetryAbortsOnContextCancelDuringDelay(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var runCalls int64
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	r.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, string, string, ...string) error {
			atomic.AddInt64(&runCalls, 1)
			cancel() // cancel before the retry delay so ctx.Done wins the select
			return apperr.Sentinel("transient")
		},
	).Times(1)

	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("mod")).
		WithMapper(workUnit).
		WithOptions(job.WithRetry(1), job.WithRetryDelay(time.Hour)).
		Build()

	results, err := j.Execute(ctx, r, "/repo")
	require.NoError(t, err, "cancellation is reported per-unit, not as a job error")
	require.Len(t, results, 1)
	assert.Equal(
		t,
		types.StatusFail,
		results[0].Status,
		"a cancelled retry yields a Fail result",
	)
	assert.Equal(
		t,
		int64(1),
		atomic.LoadInt64(&runCalls),
		"the command must run once, then the delay must abort on ctx.Done",
	)
}
