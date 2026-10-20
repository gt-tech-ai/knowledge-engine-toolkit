package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestExecutionOpsReservation_RunAfterReleaseIsInternal tests that a released
// reservation cannot run its operation.
//
// Why this test is important:
//   - Release frees the working-tree slot; a later Run on the same reservation would
//     run outside the bulkhead, racing whatever took the slot next.
//
// What it tests:
//   - Reserve, Release, Run fails with CodeInternal and the operation body never runs
func TestExecutionOpsReservation_RunAfterReleaseIsInternal(t *testing.T) {
	t.Parallel()

	var ran atomic.Bool
	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error {
			ran.Store(true)
			return nil
		},
	})
	res, err := reg.Reserve("ok", "run-1")
	require.NoError(t, err)
	res.Release()

	err = res.Run(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.False(t, ran.Load())
}

// TestExecutionOpsReserve_RejectsEmptyHolder tests that a reservation must name its
// holder.
//
// Why this test is important:
//   - An empty holder is the registry's "slot free" marker, so reserving under it would
//     leave the slot looking free to the next caller.
//
// What it tests:
//   - Reserve("ok", "") fails with CodeInvalidInput and the slot stays free
func TestExecutionOpsReserve_RejectsEmptyHolder(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	_, err := reg.Reserve("ok", "")
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.NoError(t, reg.Run(context.Background(), "ok", nil, nil))
}

// TestExecutionOpsRegistryRun_NilContextRuns tests that a run with a nil context does
// not panic.
//
// Why this test is important:
//   - A cobra command's Context() is nil when its RunE runs outside Execute (a test, an
//     embedder); the timeout decorator would panic deriving a deadline from it.
//
// What it tests:
//   - Run with a nil context succeeds
func TestExecutionOpsRegistryRun_NilContextRuns(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	var nilCtx context.Context
	require.NotPanics(t, func() {
		assert.NoError(t, reg.Run(nilCtx, "ok", nil, nil))
	})
}

// TestExecutionOpsNewFromConfig_RejectsDuplicateDescriptor tests that two descriptors
// may not share a name.
//
// Why this test is important:
//   - The second descriptor would silently replace the first's safety class and
//     timeout, so a policy could admit an operation under the wrong class.
//
// What it tests:
//   - a repeated descriptor name fails with CodeInvalidInput naming it
func TestExecutionOpsNewFromConfig_RejectsDuplicateDescriptor(t *testing.T) {
	t.Parallel()

	descs := []types.OperationDescriptor{
		opDesc("ro", types.SafetyReadOnly, types.OpLifecycleBounded),
		opDesc("ro", types.SafetyDestructive, types.OpLifecycleBounded),
	}
	_, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety:  types.SafetyCredentialed,
			Lifecycles: allLifecycles(),
		},
		Descriptors: descs,
		Builders:    mockBuilders(gomock.NewController(t), descs[:1]),
	})
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"ro"`)
}

// TestExecutionOpsRegistryRun_LogsTheMappedCode tests that the logged failure of a
// timed-out run carries CodeTimeout.
//
// Why this test is important:
//   - Logging sits outside the code map (charter §6.3), so the log line an operator
//     reads names the same code the caller gets, not the operation's raw error.
//
// What it tests:
//   - an operation that outlives its timeout logs a failure whose error is CodeTimeout
func TestExecutionOpsRegistryRun_LogsTheMappedCode(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	log := mocks.NewMockLogger(ctrl)
	log.EXPECT().WithContext(gomock.Any()).Return(log).AnyTimes()
	var logged error
	log.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes().
		Do(func(_ string, kv ...any) {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == "error" {
					logged, _ = kv[i+1].(error)
				}
			}
		})

	desc := opDesc("slow", types.SafetyReadOnly, types.OpLifecycleBounded)
	desc.Timeout = 10 * time.Millisecond
	op := mocks.NewMockOperation(ctrl)
	op.EXPECT().Descriptor().Return(desc).AnyTimes()
	op.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ any, _ interfaces.ExecutionObserver) error {
			<-ctx.Done()
			return ctx.Err()
		})
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety:  types.SafetyReadOnly,
			Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{desc},
		Builders: map[string]ops.Builder[opsEnv]{
			"slow": func(types.OperationDescriptor, opsEnv) interfaces.Operation {
				return op
			},
		},
		Log: log,
	})
	require.NoError(t, err)

	require.Error(t, reg.Run(context.Background(), "slow", nil, nil))
	require.Error(t, logged)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(logged))
}
