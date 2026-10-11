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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// execFullStack wraps a mock job with the complete decorator stack.
func execFullStack(job interfaces.AnyJob) interfaces.AnyJob {
	return decorators.Wrap(job).
		WithLogger(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		WithTracing(fixtures.NopTracer()).
		WithRecovery().
		Build()
}

// TestDecorators_RecoveryConvertsPanic verifies the recovery decorator converts a
// panicking job into a returned error + failed result instead of crashing.
//
// Why this test is important:
//   - A worker is long-running; a panic in one periodic sweep
//     run must not take down the process. Recovery is what guarantees that.
//
// What it tests:
//   - A panicking inner job, wrapped with the full stack, returns an error and a
//     failed StepResults (no panic escapes).
func TestDecorators_RecoveryConvertsPanic(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	job := mocks.NewMockAnyJob(ctrl)
	job.EXPECT().Meta().Return(types.JobMeta{Name: "relay"}).AnyTimes()
	job.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			context.Context,
			interfaces.CommandRunner,
			string,
		) (types.StepResults, error) {
			panic("boom")
		})

	results, err := execFullStack(job).Execute(context.Background(), nil, "")
	require.Error(t, err, "panic must surface as an error, not crash")
	assert.Contains(t, err.Error(), "panicked")
	require.True(t, results.HasFailures(), "panic must yield a failed result")
}

// TestDecorators_PassThrough verifies the decorated chain returns the inner job's
// results unchanged on the happy path.
//
// What it tests:
//   - A successful inner job's results pass through the full decorator stack; an
//     error is propagated.
func TestDecorators_PassThrough(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ok := mocks.NewMockAnyJob(ctrl)
	ok.EXPECT().Meta().Return(types.JobMeta{Name: "relay"}).AnyTimes()
	ok.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Name: "x", Status: types.StatusPass}}, nil)
	results, err := execFullStack(ok).Execute(context.Background(), nil, "")
	require.NoError(t, err)
	require.False(t, results.HasFailures())

	failing := mocks.NewMockAnyJob(ctrl)
	failing.EXPECT().Meta().Return(types.JobMeta{Name: "relay"}).AnyTimes()
	failing.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, apperr.Sentinel("boom"))
	_, err = execFullStack(failing).Execute(context.Background(), nil, "")
	require.Error(t, err)
}

// TestDecorators_NoDecorators verifies Wrap with no options returns the inner job.
//
// What it tests:
//   - Build() with no decorators returns the original job (backward-compatible).
func TestDecorators_NoDecorators(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	job := mocks.NewMockAnyJob(ctrl)
	assert.Same(t, interfaces.AnyJob(job), decorators.Wrap(job).Build())
}
