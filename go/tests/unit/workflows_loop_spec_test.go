package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	for _, decorated := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			var a, b atomic.Int64
			var ops []string
			var decorate loop.Decorator
			if decorated {
				decorate = func(op string, pass passWorkflow) passWorkflow {
					ops = append(ops, op)
					return pass
				}
			}
			schedule := loop.Config{Interval: time.Minute, RunAtStart: true}
			group, err := loop.NewGroupFromSpecs([]loop.Spec{
				{Run: countPass(&a), Name: "a", Op: "loop.a", Schedule: schedule},
				{Run: countPass(&b), Name: "b", Op: "loop.b", Schedule: schedule},
			}, decorate, quietLogger(t))
			require.NoError(t, err)

			require.NoError(t, group.Start(t.Context()))
			synctest.Wait()
			require.Equal(t, int64(1), a.Load())
			require.Equal(t, int64(1), b.Load())
			require.NoError(t, group.Stop(t.Context()))
			if decorated {
				require.Equal(t, []string{"loop.a", "loop.b"}, ops)
			} else {
				require.Empty(t, ops)
			}
		})
	}
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
			Run:      okPass,
			Name:     "bad",
			Op:       "loop.bad",
			Schedule: loop.Config{Interval: -time.Second},
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
