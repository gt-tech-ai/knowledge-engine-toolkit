package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cron"
)

// cronRegistry admits "drift" (read-only, long-running) and "ask" (interactive) under
// a policy that excludes "wipe" (destructive); ran records drift's decoded target.
func cronRegistry(t *testing.T, ran *string) *ops.Registry {
	t.Helper()
	run := ops.Func(func(
		_ context.Context, in *adapterInput, _ opsEnv, _ interfaces.ExecutionObserver,
	) error {
		*ran = in.Target
		return nil
	})
	descs := []types.OperationDescriptor{
		opDesc("drift", types.SafetyReadOnly, types.OpLifecycleLongRunning),
		opDesc("ask", types.SafetyReadOnly, types.OpLifecycleInteractive),
		opDesc("wipe", types.SafetyDestructive, types.OpLifecycleBounded),
	}
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
			Deny: []string{"wipe"},
		},
		Descriptors: descs,
		Builders: map[string]ops.Builder[opsEnv]{
			"drift": run,
			"ask":   run,
			"wipe":  run,
		},
	})
	require.NoError(t, err)
	return reg
}

// TestTransportCron_Run tests the scheduled-job front-end.
//
// Why this test is important:
//   - A scheduled job runs unattended, often under cloud credentials: it must run only
//     what its policy admits, never an operation that waits for a human, and never on
//     arguments it did not understand.
//
// What it tests:
//   - An admitted operation runs with its strictly decoded arguments; an operation the
//     policy excludes is FORBIDDEN, an unknown one NOT_FOUND, an interactive one
//     INVALID_INPUT, and an unknown argument INVALID_INPUT; none of these run.
func TestTransportCron_Run(t *testing.T) {
	t.Parallel()
	var ran string
	reg := cronRegistry(t, &ran)
	require.NoError(
		t,
		cron.Run(t.Context(), reg, "drift", []byte(`{"target":"live"}`), nil),
	)
	assert.Equal(t, "live", ran)

	ran = ""
	for name, want := range map[string]apperr.ErrorCode{
		"wipe": apperr.CodeForbidden, "nope": apperr.CodeNotFound,
		"ask": apperr.CodeInvalidInput,
	} {
		err := cron.Run(t.Context(), reg, name, nil, nil)
		assert.Equal(t, want, apperr.Code(err), name)
	}
	err := cron.Run(t.Context(), reg, "drift", []byte(`{"targt":"x"}`), nil)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Empty(t, ran)
}
