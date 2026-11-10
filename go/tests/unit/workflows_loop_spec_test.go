package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/loop"
)

// TestNewGroupFromSpecs_DecoratesAndRunsEverySpec tests building a Group from Specs.
//
// Why this test is important:
//   - A service declares its loops as Specs; a pass that skipped the decorator would run
//     unlogged and unrecovered, and a dropped spec would silently never run.
//
// What it tests:
//   - Every spec's pass runs on its schedule, wrapped by the decorator under its Op.
//   - A nil decorator runs the passes undecorated.
func TestNewGroupFromSpecs_DecoratesAndRunsEverySpec(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var ops []string

		testGroupRunsBothSpecs(t, func(op string, pass passWorkflow) passWorkflow {
			ops = append(ops, op)

			return pass
		})
		require.Equal(t, []string{"loop.a", "loop.b"}, ops)
	})
	synctest.Test(t, func(t *testing.T) { testGroupRunsBothSpecs(t, nil) })
}

// testGroupRunsBothSpecs starts a group of two specs under decorate and checks each
// runs once.
func testGroupRunsBothSpecs(t *testing.T, decorate loop.Decorator) {
	t.Helper()

	var int643, secondCount atomic.Int64

	schedule := loop.Config{Interval: time.Minute, RunAtStart: true, ErrorBackoff: 0}
	group, err := loop.NewGroupFromSpecs([]loop.Spec{
		{Run: countPass(&int643), Name: textA, Op: "loop.a", Schedule: schedule},
		{Run: countPass(&secondCount), Name: textB, Op: "loop.b", Schedule: schedule},
	}, decorate, quietLogger(t))
	require.NoError(t, err)

	require.NoError(t, group.Start(t.Context()))
	synctest.Wait()
	require.Equal(t, int64(1), int643.Load())
	require.Equal(t, int64(1), secondCount.Load())
	require.NoError(t, group.Stop(t.Context()))
}

// TestNewGroupFromSpecs_RejectsAnInvalidSchedule tests schedule validation.
//
// Why this test is important:
//   - A negative interval would turn a periodic pass into a hot loop; the
//     composition root must refuse it at boot.
//
// What it tests:
//   - A spec with a negative Interval is CodeInvalidInput.
func TestNewGroupFromSpecs_RejectsAnInvalidSchedule(t *testing.T) {
	t.Parallel()

	_, err := loop.NewGroupFromSpecs([]loop.Spec{
		{
			Run:  okPass,
			Name: textBad,
			Op:   "loop.bad",
			Schedule: loop.Config{
				Interval:     -time.Second,
				ErrorBackoff: 0,
				RunAtStart:   false,
			},
		},
	}, nil, quietLogger(t))
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// passWorkflow is the workflow shape of one loop pass.
type passWorkflow = interfaces.Workflow[struct{}, struct{}]

// countPass returns a pass that counts its runs into n.
func countPass(n *atomic.Int64) func(context.Context) error {
	return func(context.Context) error {
		n.Add(1)

		return nil
	}
}
