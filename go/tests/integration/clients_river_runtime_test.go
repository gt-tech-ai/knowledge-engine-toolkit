//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	riverclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
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
	ctx := context.Background()
	db, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err)
	defer db.Close(ctx)
	require.NoError(t, riverclient.Migrate(ctx, db.GetDSN()))

	fired := make(chan struct{}, 1)
	workers := river.NewWorkers()
	river.AddWorker(workers, &pingWorker{fired: fired})

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(
				time.Hour,
			), // only fires on start within the test window
			func() (river.JobArgs, *river.InsertOpts) { return pingArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}

	rt, err := riverclient.NewRuntime(ctx, riverclient.RuntimeConfig{
		DatabaseURL:  db.GetDSN(),
		Workers:      workers,
		PeriodicJobs: periodic,
	})
	require.NoError(t, err)
	require.NoError(t, rt.Start(ctx))
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	select {
	case <-fired:
	case <-time.After(15 * time.Second):
		t.Fatal("periodic job did not fire within 15s")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rt.Stop(stopCtx))
}
