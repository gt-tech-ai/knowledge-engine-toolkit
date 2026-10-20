package unit_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/runs"
)

// seqIDs returns a run-id source yielding run-1, run-2, … safely across goroutines.
func seqIDs() func() string {
	var n atomic.Int64
	return func() string { return fmt.Sprintf("run-%d", n.Add(1)) }
}

// heldRegistry returns a registry whose "hold" op reports one PASS step named "lint",
// signals started, and blocks until release is closed or its context ends (returning
// the context error), plus an "ok" op that succeeds at once; d is every op's timeout.
func heldRegistry(
	t *testing.T,
	d time.Duration,
) (reg *ops.Registry, started, release chan struct{}) {
	t.Helper()
	started, release = make(chan struct{}), make(chan struct{})
	reg = funcRegistry(t, d, map[string]runFunc{
		"hold": func(ctx context.Context, _ any, obs interfaces.ExecutionObserver) error {
			obs.OnStepComplete(types.StepResult{Name: "lint", Status: types.StatusPass})
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		"ok": func(context.Context, any, interfaces.ExecutionObserver) error { return nil },
	})
	return reg, started, release
}

// TestExecutionRunsStart_ReturnsRunIDImmediately tests that Start hands back a run id
// while the operation is still running, detached from the caller's context.
//
// Why this test is important:
//   - A long-running operation (test.unit, a full gate) outlives the MCP tool call
//     that started it; the call must return at once and its context ending must not
//     kill the run.
//
// What it tests:
//   - Start returns "run-1" while "hold" blocks; Status reports running for op "hold";
//     cancelling Start's context leaves it running; releasing it ends it succeeded
func TestExecutionRunsStart_ReturnsRunIDImmediately(t *testing.T) {
	t.Parallel()

	reg, started, release := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), nil, 0)
	ctx, cancel := context.WithCancel(context.Background())

	id, err := table.Start(ctx, "hold", nil)
	require.NoError(t, err)
	assert.Equal(t, "run-1", id)
	<-started
	cancel()

	snap, err := table.Status(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateRunning, snap.State)
	assert.Equal(t, "hold", snap.Op)

	close(release)
	table.Wait()
	snap, err = table.Status(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateSucceeded, snap.State)
}

// TestExecutionRunsStatus_TransitionsRunningToSucceeded tests a run's snapshot over its
// life: its steps while running, and its result, log paths and listing once it ends.
//
// Why this test is important:
//   - An agent polls runs.status and then reads runs.result; both are built from these
//     snapshots, so a missing step or log path would hide what the run did.
//
// What it tests:
//   - while running, Status lists the reported "lint" step and Result fails with
//     CodeConflict; after release the run is succeeded with an end time, Result
//     carries onDone's log paths, and List holds the one run
//   - Status, Result and Cancel of an unknown id fail with CodeNotFound
func TestExecutionRunsStatus_TransitionsRunningToSucceeded(t *testing.T) {
	t.Parallel()

	reg, started, release := heldRegistry(t, time.Minute)
	var finished atomic.Int64
	table := runs.New(reg, seqIDs(), func(s runs.Snapshot) ([]string, error) {
		finished.Add(1)
		return []string{"/runs/" + s.ID + "/lint.log"}, nil
	}, 0)

	id, err := table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)
	<-started

	snap, err := table.Status(id)
	require.NoError(t, err)
	require.Len(t, snap.Steps, 1)
	assert.Equal(t, "lint", snap.Steps[0].Name)
	_, err = table.Result(id)
	assert.Equal(t, apperr.CodeConflict, apperr.Code(err))

	close(release)
	table.Wait()
	snap, err = table.Result(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateSucceeded, snap.State)
	require.NoError(t, snap.Err)
	assert.False(t, snap.Ended.Before(snap.Started))
	assert.Equal(t, []string{"/runs/run-1/lint.log"}, snap.LogPaths)
	assert.Equal(t, int64(1), finished.Load())

	list := table.List()
	require.Len(t, list, 1)
	assert.Equal(t, id, list[0].ID)

	_, err = table.Status("nope")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
	_, err = table.Result("nope")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(table.Cancel("nope")))
}

// TestExecutionRunsCancel_EndsRunAsCancelled tests that cancelling a run stops its
// operation and records it as cancelled rather than failed.
//
// Why this test is important:
//   - An agent that started the wrong run must be able to stop it and free the working
//     tree; a cancel is the agent's choice, not an operation failure.
//
// What it tests:
//   - Cancel on a blocking run ends it cancelled with a CodeCanceled error; a second
//     Cancel of the finished run is a no-op; the slot is free again afterwards
func TestExecutionRunsCancel_EndsRunAsCancelled(t *testing.T) {
	t.Parallel()

	reg, started, _ := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), nil, 0)
	id, err := table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)
	<-started

	require.NoError(t, table.Cancel(id))
	table.Wait()
	snap, err := table.Status(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateCancelled, snap.State)
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(snap.Err))
	require.NoError(t, table.Cancel(id))
	require.NoError(t, reg.Run(context.Background(), "ok", nil, nil))
}

// TestExecutionRunsTimeout_EndsRunAsFailedWithCodeTimeout tests that a run exceeding its
// operation's timeout ends failed with the timeout's coded error.
//
// Why this test is important:
//   - A wedged background run would hold the working-tree slot forever; the
//     descriptor's timeout must end it, and the agent must see it as a failure.
//
// What it tests:
//   - with a 10ms timeout, "hold" ends failed with CodeTimeout
func TestExecutionRunsTimeout_EndsRunAsFailedWithCodeTimeout(t *testing.T) {
	t.Parallel()

	reg, _, _ := heldRegistry(t, 10*time.Millisecond)
	table := runs.New(reg, seqIDs(), nil, 0)
	id, err := table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)

	table.Wait()
	snap, err := table.Status(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateFailed, snap.State)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(snap.Err))
}

// TestExecutionRunsCancelAll_StopsActiveRunsAndRefusesNewOnes tests the table's shutdown
// path.
//
// Why this test is important:
//   - When the MCP client disconnects, no background run may outlive the server or
//     start after it has begun shutting down.
//
// What it tests:
//   - CancelAll ends the active run cancelled; a later Start fails with
//     CodeUnavailable and leaves the slot free
func TestExecutionRunsCancelAll_StopsActiveRunsAndRefusesNewOnes(t *testing.T) {
	t.Parallel()

	reg, started, _ := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), nil, 0)
	id, err := table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)
	<-started

	table.CancelAll()
	table.Wait()
	snap, err := table.Status(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateCancelled, snap.State)

	_, err = table.Start(context.Background(), "ok", nil)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	require.NoError(t, reg.Run(context.Background(), "ok", nil, nil))
}

// TestExecutionRunsSecondStart_WhileActiveReturnsBusyWithRunID tests that the
// working-tree slot a run holds is named by its run id.
//
// Why this test is important:
//   - Operations share one working tree; an agent told "busy" must learn which run to
//     poll or cancel, whether it tried another background run or a bounded tool.
//
// What it tests:
//   - while run-1 holds the slot, Start("ok") and a bounded Run("ok") both fail with
//     CodeUnavailable "busy: run-1 is running"; once it ends, Start("ok") succeeds
func TestExecutionRunsSecondStart_WhileActiveReturnsBusyWithRunID(t *testing.T) {
	t.Parallel()

	reg, started, release := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), nil, 0)
	_, err := table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)
	<-started

	_, err = table.Start(context.Background(), "ok", nil)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	assert.Contains(t, err.Error(), "busy: run-1 is running")
	err = reg.Run(context.Background(), "ok", nil, nil)
	assert.Contains(t, err.Error(), "busy: run-1 is running")

	close(release)
	table.Wait()
	_, err = table.Start(context.Background(), "ok", nil)
	require.NoError(t, err)
	table.Wait()
}

// TestExecutionRunsOnDoneError_JoinsRunError tests that a failure persisting a run's
// logs reaches the run's outcome without hiding the operation's own error.
//
// Why this test is important:
//   - A run whose logs were never written must not read as a clean success, and a run
//     that already failed must still report why it failed, not only that its logs
//     could not be written.
//
// What it tests:
//   - an "ok" run whose onDone fails ends failed with onDone's CodeInternal error
//   - a "hold" run that times out and whose onDone fails ends failed with CodeTimeout,
//     and its error also carries onDone's message
func TestExecutionRunsOnDoneError_JoinsRunError(t *testing.T) {
	t.Parallel()

	onDone := func(runs.Snapshot) ([]string, error) {
		return nil, apperr.New(apperr.CodeInternal, "write step logs")
	}

	reg, _, _ := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), onDone, 0)
	id, err := table.Start(context.Background(), "ok", nil)
	require.NoError(t, err)
	table.Wait()
	snap, err := table.Result(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateFailed, snap.State)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(snap.Err))

	reg, _, _ = heldRegistry(t, 10*time.Millisecond)
	table = runs.New(reg, seqIDs(), onDone, 0)
	id, err = table.Start(context.Background(), "hold", nil)
	require.NoError(t, err)
	table.Wait()
	snap, err = table.Result(id)
	require.NoError(t, err)
	assert.Equal(t, runs.StateFailed, snap.State)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(snap.Err))
	assert.Contains(t, snap.Err.Error(), "write step logs")
}

// TestExecutionRunsRetention_ForgetsOldestFinishedRuns tests that the table remembers
// only the newest keep finished runs.
//
// Why this test is important:
//   - A long-lived MCP session starts many runs, each holding its steps' captured
//     output; an unbounded table grows for as long as the server lives.
//
// What it tests:
//   - with keep 2, after three sequential runs List holds run-2 and run-3 and
//     Status("run-1") fails with CodeNotFound
func TestExecutionRunsRetention_ForgetsOldestFinishedRuns(t *testing.T) {
	t.Parallel()

	reg, _, _ := heldRegistry(t, time.Minute)
	table := runs.New(reg, seqIDs(), nil, 2)
	for range 3 {
		_, err := table.Start(context.Background(), "ok", nil)
		require.NoError(t, err)
		table.Wait()
	}

	ids := make([]string, 0, len(table.List()))
	for _, s := range table.List() {
		ids = append(ids, s.ID)
	}
	assert.Equal(t, []string{"run-2", "run-3"}, ids)
	_, err := table.Status("run-1")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

// TestExecutionRunsStart_DuplicateRunIDFailsInternal tests that a run id the table
// already holds is refused rather than overwriting the earlier run.
//
// Why this test is important:
//   - Overwriting would make Status of the old id answer for the new run and list one
//     run twice; a repeated id is a broken id source, not an agent error.
//
// What it tests:
//   - with an id source that always yields "dup", the second Start fails with
//     CodeInternal, List still holds one run, and the slot is free afterwards
func TestExecutionRunsStart_DuplicateRunIDFailsInternal(t *testing.T) {
	t.Parallel()

	reg, _, _ := heldRegistry(t, time.Minute)
	table := runs.New(reg, func() string { return "dup" }, nil, 0)
	_, err := table.Start(context.Background(), "ok", nil)
	require.NoError(t, err)
	table.Wait()

	_, err = table.Start(context.Background(), "ok", nil)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Len(t, table.List(), 1)
	require.NoError(t, reg.Run(context.Background(), "ok", nil, nil))
}
