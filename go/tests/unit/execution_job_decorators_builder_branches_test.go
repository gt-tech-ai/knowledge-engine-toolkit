package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestMetricsDecorator_RecordsFailedOutcomeWhenResultsFail tests that the metrics
// decorator labels a run "failed" when the inner job returns failing results
// without returning an error, distinguishing it from both a clean run and a hard
// error.
//
// Why this test is important:
//   - A job can complete "successfully" (nil error) while its step results contain
//     failures — that is the normal shape of a lint/test job that ran fine but
//     found problems. The run-outcome metric must record that as "failed", not
//     "ok", or dashboards and alerts would under-count real failures that never
//     raised an error. This pins the results-failed branch of the outcome
//     classification, separate from the error branch.
//
// What it tests:
//   - Wrapping a job that returns a StatusFail result with a nil error and running
//     it records the run counter with the outcome label "failed", while the
//     failing results still pass through unchanged.
func TestMetricsDecorator_RecordsFailedOutcomeWhenResultsFail(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	// A spy counter that captures the outcome label the decorator records; the last
	// label value is the outcome ("job", outcome) per the production Add call.
	var gotOutcome string
	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Add(gomock.Any(), gomock.Any()).Do(
		func(_ float64, labels ...string) {
			if len(labels) > 0 {
				gotOutcome = labels[len(labels)-1]
			}
		},
	).AnyTimes()

	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()

	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).AnyTimes()
	metrics.EXPECT().Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()

	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(types.JobMeta{Name: "lint"}).AnyTimes()
	inner.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Name: "lint", Status: types.StatusFail, Error: "bad"}}, nil).
		Times(1)

	j := decorators.Wrap(inner).WithMetrics(metrics).Build()
	results, err := j.Execute(context.Background(), nil, "/root")

	require.NoError(t, err, "failing results without an error are not a job error")
	require.True(t, results.HasFailures(), "failing results must pass through unchanged")
	assert.Equal(
		t,
		"failed",
		gotOutcome,
		"a nil-error run with failing results must be labelled failed",
	)
}

// TestTracingDecorator_MetaDelegatesToInner tests that the tracing decorator
// reports the wrapped job's identity through Meta unchanged.
//
// Why this test is important:
//   - Meta is how phases, observers, and the renderer label a job; the tracing
//     decorator (the outermost layer when tracing is enabled) must be transparent
//     to identity or a traced job would show up mislabeled in gate output and lose
//     the name everything else keys off. This pins the delegating Meta on the
//     tracing wrapper specifically.
//
// What it tests:
//   - A job wrapped with WithTracing reports the inner job's name from Meta().
func TestTracingDecorator_MetaDelegatesToInner(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(types.JobMeta{Name: "vet", Group: "quality"}).AnyTimes()

	traced := decorators.Wrap(inner).
		WithTracing(fixtures.NopTracer()).
		Build()

	meta := traced.Meta()
	assert.Equal(t, "vet", meta.Name, "tracing decorator must delegate the job name")
	assert.Equal(
		t,
		"quality",
		meta.Group,
		"tracing decorator must delegate the job group",
	)
}
