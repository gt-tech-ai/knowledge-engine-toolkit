package runs

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Snapshot is a point-in-time copy of one run.
type Snapshot struct {
	// Started is when Start launched the run.
	Started time.Time
	// Ended is when the operation returned; zero while running.
	Ended time.Time
	// Err is the operation's coded error; nil while running or on success.
	Err error
	// LogPaths is the log paths onDone returned, aligned with the front-end's
	// flattened view of Steps; nil while running.
	LogPaths []string
	// ID is the run id Start returned.
	ID string
	// Op is the operation's name.
	Op string
	// State is where the run is in its life.
	State State
	// Steps is every step the operation has completed so far, in order, a group step
	// with its children nested.
	Steps []types.StepResult
}

// run is one background run's record; its mutable fields are guarded by the table's
// mutex, and its observer by its own.
type run struct {
	// started is when Start launched the run.
	started time.Time
	// ended is when the operation returned.
	ended time.Time
	// err is the operation's error.
	err error
	// cancel cancels the run's context.
	cancel context.CancelFunc
	// logPaths is the log paths onDone returned.
	logPaths []string
	// id is the run id.
	id string
	// op is the operation's name.
	op string
	// state is where the run is in its life.
	state State
	// obs records the steps the operation completes.
	obs Collector
}

// snapshot copies r; the caller holds the table's mutex (or owns r exclusively).
func (r *run) snapshot() Snapshot {
	return cloneSnapshot(Snapshot{
		ID: r.id, Op: r.op, State: r.state, Err: r.err, Steps: r.obs.Steps(),
		LogPaths: r.logPaths, Started: r.started, Ended: r.ended,
	})
}
