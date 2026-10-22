package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// blockingRegistry returns a registry with a "hold" op that signals started and then
// blocks until release is closed, and an "ok" op that succeeds at once.
func blockingRegistry(
	t *testing.T,
) (reg *ops.Registry, started, release chan struct{}) {
	t.Helper()
	started, release = make(chan struct{}), make(chan struct{})
	r := funcRegistry(t, time.Minute, map[string]runFunc{
		"hold": func(context.Context, any, interfaces.ExecutionObserver) error {
			close(started)
			<-release
			return nil
		},
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	return r, started, release
}

// TestExecutionOpsBulkhead_RejectsSecondWorkingTreeOp tests that a second operation
// started while one runs fails at once naming the running one.
//
// Why this test is important:
//   - Operations share one working tree; two at once (a lint --fix during a gen) would
//     race on the same files. An agent must learn what holds the slot, not hang.
//
// What it tests:
//   - while "hold" runs, Run("ok") fails with CodeUnavailable containing "hold"
func TestExecutionOpsBulkhead_RejectsSecondWorkingTreeOp(t *testing.T) {
	t.Parallel()

	reg, started, release := blockingRegistry(t)
	done := make(chan error, 1)
	go func() { done <- reg.Run(context.Background(), "hold", nil, nil) }()
	<-started

	err := reg.Run(context.Background(), "ok", nil, nil)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	assert.Contains(t, err.Error(), "busy: hold is running")

	close(release)
	require.NoError(t, <-done)
}

// TestExecutionOpsBulkhead_ReleasesAfterRun tests that the slot frees when a run ends,
// and that a reservation holds it until used or released.
//
// Why this test is important:
//   - A slot that never frees would wedge every later operation; a background run (56.5)
//     reserves before it starts, so the reservation itself must hold and free the slot.
//
// What it tests:
//   - after "hold" finishes, Run("ok") succeeds; a Reservation blocks Run("ok") under
//     its holder's name until Released; a used Reservation cannot run twice
func TestExecutionOpsBulkhead_ReleasesAfterRun(t *testing.T) {
	t.Parallel()

	r := funcRegistry(t, time.Minute, map[string]runFunc{
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	require.NoError(t, r.Run(context.Background(), "ok", nil, nil))

	res, err := r.Reserve("ok", "run-1")
	require.NoError(t, err)
	err = r.Run(context.Background(), "ok", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "busy: run-1 is running")
	res.Release()
	res.Release()
	require.NoError(t, r.Run(context.Background(), "ok", nil, nil))

	res, err = r.Reserve("ok", "run-2")
	require.NoError(t, err)
	require.NoError(t, res.Run(context.Background(), nil, nil))
	require.Error(t, res.Run(context.Background(), nil, nil))
	require.NoError(t, r.Run(context.Background(), "ok", nil, nil))

	_, err = r.Reserve("rm", "x")
	assert.Equal(t, apperr.CodeForbidden, apperr.Code(err))
}
