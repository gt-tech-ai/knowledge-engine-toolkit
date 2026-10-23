package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestJobTier_OrdersRecoveryOutermost tests the job decoration tier.
//
// Why this test is important:
//   - A job is decorated once, at the tier; recovery must sit outermost so a panic in
//     any later layer still fails the job instead of crashing the gate.
//
// What it tests:
//   - The tier is named "job" and orders recovery → logging → tracing → metrics →
//     timeout.
func TestJobTier_OrdersRecoveryOutermost(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "job", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerLogging,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerTimeout,
	}}, job.Tier())
}

// TestJobBase_FuncRunsAgainstTheRunnerAndIsObserved tests a function job.
//
// Why this test is important:
//   - Func is how a command becomes a gate job; the job must run against the gate's
//     runner and root, carry its name and group into the results table, and be one span
//     per run so a slow gate is diagnosable.
//
// What it tests:
//   - Meta carries the name and group.
//   - Execute hands run the gate's runner and root and returns run's results and error.
//   - With the tier observed, one span named "<job>.execute" covers the run.
func TestJobBase_FuncRunsAgainstTheRunnerAndIsObserved(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End().Times(2)
	tracer.EXPECT().
		Start(gomock.Any(), "lint.execute", gomock.Any()).
		Times(2).
		DoAndReturn(
			func(ctx context.Context, _ string, _ ...interfaces.SpanOption) (
				context.Context, interfaces.Span,
			) {
				return ctx, span
			},
		)
	boom := apperr.New(apperr.CodeQualityFailed, "lint failed")
	fail := false
	lint := fixtures.Must(job.NewBase(decorate.Fixed(decorate.Deps{
		Observability: decorate.Observability{Tracer: tracer},
	}), "lint")).Func("lint", "Static", func(
		_ context.Context, r interfaces.CommandRunner, root string,
	) (types.StepResults, error) {
		assert.Same(t, runner, r)
		if fail {
			return nil, boom
		}
		return types.StepResults{{Name: root, Status: types.StatusPass}}, nil
	})

	assert.Equal(t, types.JobMeta{Name: "lint", Group: "Static"}, lint.Meta())
	got, err := lint.Execute(t.Context(), runner, "/repo")
	require.NoError(t, err)
	assert.Equal(t, types.StepResults{{Name: "/repo", Status: types.StatusPass}}, got)

	fail = true
	_, err = lint.Execute(t.Context(), runner, "/repo")
	require.ErrorIs(t, err, boom)
}

// TestJobBase_LocalAndWrapKeepMetaAndRecover tests adapting existing work.
//
// Why this test is important:
//   - An in-process unit (a seeder, a sync) runs as a gate job without a runner, and an
//     existing job gains the tier without losing the tool its results table shows; a
//     panic in either must fail the job with a coded error, not crash the process.
//
// What it tests:
//   - Local carries its name and group and returns its work's results.
//   - Wrap keeps the inner job's full metadata (tool included).
//   - A panicking job built over undecorated Decorations returns an INTERNAL error:
//     recovery is the tier's outermost layer.
func TestJobBase_LocalAndWrapKeepMetaAndRecover(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	meta := types.JobMeta{Name: "seed", Group: "Seed", Tool: "psql"}
	results := types.StepResults{{Name: "seed", Status: types.StatusPass}}

	seed := job.Base{}.Local(
		"seed", "Seed",
		func(context.Context) (types.StepResults, error) { return results, nil },
	)
	assert.Equal(t, types.JobMeta{Name: "seed", Group: "Seed"}, seed.Meta())
	got, err := seed.Execute(t.Context(), nil, "")
	require.NoError(t, err)
	assert.Equal(t, results, got)

	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(meta)
	inner.EXPECT().Execute(gomock.Any(), gomock.Any(), "/repo").DoAndReturn(
		func(context.Context, interfaces.CommandRunner, string) (types.StepResults, error) {
			panic("boom")
		},
	)
	wrapped := fixtures.Must(job.NewBase(fixtures.Decorations(), "wrapped")).Wrap(inner)
	assert.Equal(t, meta, wrapped.Meta())
	_, err = wrapped.Execute(t.Context(), nil, "/repo")
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestJobBase_CheckReportsOneStep tests a check job.
//
// Why this test is important:
//   - Most CLI jobs are one pass/fail check; Check makes that a one-step job without a
//     hand-written results table, so the step must carry the job's name and the error.
//
// What it tests:
//   - A nil error is one Pass step named after the job.
//   - An error is one Fail step carrying the error's message, and no execution error.
func TestJobBase_CheckReportsOneStep(t *testing.T) {
	t.Parallel()
	pass := job.Base{}.Check("fmt", "Go", func(context.Context) error { return nil })
	got, err := pass.Execute(context.Background(), nil, "")
	require.NoError(t, err)
	assert.Equal(t, types.JobMeta{Name: "fmt", Group: "Go"}, pass.Meta())
	assert.Equal(t, types.StepResults{{Name: "fmt", Status: types.StatusPass}}, got)

	fail := job.Base{}.Check("fmt", "Go", func(context.Context) error {
		return apperr.New(apperr.CodeInvalidInput, "unformatted")
	})
	got, err = fail.Execute(context.Background(), nil, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusFail, got[0].Status)
	assert.Contains(t, got[0].Error, "unformatted")
}

// TestJobWarn_DowngradesFailures tests a warn-only job.
//
// Why this test is important:
//   - An advisory job must never fail its gate, but an execution error (the job could
//     not run at all) is not a finding and must still reach the gate.
//
// What it tests:
//   - Warn keeps the job's meta and turns each Fail step into Warn, leaving Pass steps.
//   - An execution error passes through unchanged.
func TestJobWarn_DowngradesFailures(t *testing.T) {
	t.Parallel()
	inner := job.Base{}.Func("doctor", "Setup", func(
		context.Context, interfaces.CommandRunner, string,
	) (types.StepResults, error) {
		return types.StepResults{
			{Name: "a", Status: types.StatusPass},
			{Name: "b", Status: types.StatusFail, Error: "missing"},
		}, nil
	})
	w := job.Warn(inner)
	got, err := w.Execute(context.Background(), nil, "")
	require.NoError(t, err)
	assert.Equal(t, inner.Meta(), w.Meta())
	assert.Equal(t, types.StepResults{
		{Name: "a", Status: types.StatusPass},
		{Name: "b", Status: types.StatusWarn, Error: "missing"},
	}, got)

	boom := apperr.New(apperr.CodeUnavailable, "no runner")
	broken := job.Warn(job.Base{}.Func("x", "y", func(
		context.Context, interfaces.CommandRunner, string,
	) (types.StepResults, error) {
		return nil, boom
	}))
	_, err = broken.Execute(context.Background(), nil, "")
	assert.ErrorIs(t, err, boom)
}

// TestJobComponent_SnakeCasesGroupAndName tests the job component name.
//
// Why this test is important:
//   - The component name keys a job's decorations in config; it must be stable and
//     config-friendly whatever the human-readable name and group look like.
//
// What it tests:
//   - The prefix, then the snake_case group and name, joined with dots.
func TestJobComponent_SnakeCasesGroupAndName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "cli.setup.git_hooks", job.Component("cli", "Setup", "git hooks"))
	assert.Equal(t, "cli.go.fmt_check", job.Component("cli", "Go!", "fmt / check"))
}
