//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	riverclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
)

// pingArgs is a trivial periodic job used to prove the River runtime schedules and
// works jobs.
type pingArgs struct{}

// Kind identifies the job type for River.
func (pingArgs) Kind() string { return "test_ping" }

// pingWorker signals a channel each time it works a pingArgs job.
type pingWorker struct {
	river.WorkerDefaults[pingArgs]

	fired chan struct{}
}

// Work signals that the periodic ping fired.
func (w *pingWorker) Work(_ context.Context, _ *river.Job[pingArgs]) error {
	select {
	case w.fired <- struct{}{}:
	default:
	}

	return nil
}

// TestRiverRuntime_PeriodicJobFiresAndStops verifies the real River runtime
// schedules a RunOnStart periodic job, works it, and stops cleanly.
//
// Why this test is important:
//   - A worker's periodic sweeps run as River periodic jobs; if the
//     runtime doesn't schedule + work them, nothing drains the outbox. Leader
//     election (river_leader) must be active so periodics fire once cluster-wide.
//
// What it tests:
//   - A RunOnStart periodic job is worked after Start; a river_leader row exists
//     (leader elected); Stop returns without error.
func TestRiverRuntime_PeriodicJobFiresAndStops(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testDatabase, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err)

	defer testDatabase.Close(ctx)

	require.NoError(t, riverclient.Migrate(ctx, testDatabase.GetDSN()))

	fired := make(chan struct{}, 1)
	workers := river.NewWorkers()
	river.AddWorker(
		workers,
		&pingWorker{fired: fired, WorkerDefaults: river.WorkerDefaults[pingArgs]{}},
	)

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(
				time.Hour,
			), // only fires on start within the test window
			func() (river.JobArgs, *river.InsertOpts) { return pingArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true, ID: ""},
		),
	}

	runtime, err := riverclient.NewRuntime(
		ctx,
		fixtures.BuildRef(func(row *riverclient.RuntimeConfig) {
			row.DatabaseURL = testDatabase.GetDSN()
			row.Workers = workers
			row.PeriodicJobs = periodic
		}),
	)
	require.NoError(t, err)
	require.NoError(t, runtime.Start(ctx))
	t.Cleanup(func() { _ = runtime.Stop(context.Background()) })

	select {
	case <-fired:
	case <-time.After(15 * time.Second):
		t.Fatal("periodic job did not fire within 15s")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, runtime.Stop(stopCtx))
}
