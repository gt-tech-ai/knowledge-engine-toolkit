package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// runFunc is the body a mock operation's Run executes.
type runFunc func(ctx context.Context, in any, obs interfaces.ExecutionObserver) error

// funcRegistry builds a registry admitting every op in runs (read-only, bounded, timeout
// d) whose Run executes the given body, plus an inadmissible destructive op "rm".
func funcRegistry(
	t *testing.T,
	d time.Duration,
	runs map[string]runFunc,
) *ops.Registry {
	t.Helper()
	ctrl := gomock.NewController(t)
	descs := make([]types.OperationDescriptor, 0, 1+len(runs))
	descs = append(descs,
		opDesc("rm", types.SafetyDestructive, types.OpLifecycleBounded),
	)
	builders := map[string]ops.Builder[opsEnv]{"rm": mockBuilders(ctrl, descs)["rm"]}
	for name, run := range runs {
		desc := opDesc(name, types.SafetyReadOnly, types.OpLifecycleBounded)
		desc.Timeout = d
		descs = append(descs, desc)
		builders[name] = func(
			desc types.OperationDescriptor,
			_ opsEnv,
		) interfaces.Operation {
			op := mocks.NewMockOperation(ctrl)
			op.EXPECT().Descriptor().Return(desc).AnyTimes()
			op.EXPECT().
				Run(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(run).
				AnyTimes()
			return op
		}
	}
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety:  types.SafetyMutating,
			Lifecycles: allLifecycles(),
		},
		Descriptors: descs,
		Builders:    builders,
		Log:         fixtures.NopLogger(),
	})
	require.NoError(t, err)
	return reg
}

// TestExecutionOpsRegistryRun_TimeoutIsCodeTimeout tests that an operation outliving its
// descriptor timeout fails with CodeTimeout.
//
// Why this test is important:
//   - A front-end with no operator (MCP, a CronJob) relies on the descriptor timeout to
//     bound every run; the failure must be classifiable as a timeout, not a generic
//     error.
//
// What it tests:
//   - an op blocking on its context under a 10ms timeout fails with CodeTimeout naming
//     the op and the timeout
func TestExecutionOpsRegistryRun_TimeoutIsCodeTimeout(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, 10*time.Millisecond, map[string]runFunc{
		"slow": func(ctx context.Context, _ any, _ interfaces.ExecutionObserver) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	err := reg.Run(context.Background(), "slow", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
	assert.Contains(t, err.Error(), "slow")
	assert.Contains(t, err.Error(), "10ms")
}

// TestExecutionOpsRegistryRun_CanceledIsCodeCanceled tests that a caller cancelling the
// context yields CodeCanceled.
//
// Why this test is important:
//   - A cancelled run (Ctrl-C, an MCP client disconnect) is not a failure of the
//     operation; front-ends report it differently.
//
// What it tests:
//   - an op returning the context error after the caller cancels fails with CodeCanceled
func TestExecutionOpsRegistryRun_CanceledIsCodeCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"stop": func(ctx context.Context, _ any, _ interfaces.ExecutionObserver) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	})
	err := reg.Run(ctx, "stop", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))
}

// TestExecutionOpsRegistryRun_PanicIsCodeInternal tests that a panicking operation is
// recovered.
//
// Why this test is important:
//   - A long-lived front-end (the MCP server) must survive a panic in one operation.
//
// What it tests:
//   - an op that panics fails with CodeInternal instead of unwinding
func TestExecutionOpsRegistryRun_PanicIsCodeInternal(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"boom": func(context.Context, any, interfaces.ExecutionObserver) error {
			panic("boom")
		},
	})
	err := reg.Run(context.Background(), "boom", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestExecutionOpsRegistryRun_UnknownIsNotFound tests how Run reports a name it cannot
// run.
//
// Why this test is important:
//   - An agent asking for a missing tool and one asking for a forbidden tool need
//     different answers: the first is a typo, the second a policy decision.
//
// What it tests:
//   - an undescribed name fails with CodeNotFound; a described but excluded name fails
//     with CodeForbidden
func TestExecutionOpsRegistryRun_UnknownIsNotFound(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, time.Minute, nil)

	err := reg.Run(context.Background(), "nope", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	err = reg.Run(context.Background(), "rm", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeForbidden, apperr.Code(err))
}

// TestExecutionOpsRegistryRun_PreservesErrorCode tests that the decorator chain leaves an
// operation's own error code alone.
//
// Why this test is important:
//   - The CLI maps error codes to exit codes; routing commands through the registry
//     must not shift them.
//
// What it tests:
//   - an op returning CodeInvalidInput fails with CodeInvalidInput; a succeeding op
//     returns nil
func TestExecutionOpsRegistryRun_PreservesErrorCode(t *testing.T) {
	t.Parallel()

	reg := funcRegistry(t, time.Minute, map[string]runFunc{
		"bad": func(context.Context, any, interfaces.ExecutionObserver) error {
			return apperr.New(apperr.CodeInvalidInput, "bad flag")
		},
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	err := reg.Run(context.Background(), "bad", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "bad flag")

	require.NoError(t, reg.Run(context.Background(), "ok", nil, nil))
}
