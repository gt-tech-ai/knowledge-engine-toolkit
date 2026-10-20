// Package runs is the background-run table a front-end uses for long_running
// operations: Start reserves the operations registry's working-tree slot under a new run
// id and runs the operation in a goroutine, and Status / Result / Cancel / List address
// the run by that id afterwards. The table is front-end-neutral — the MCP server is its
// first user — and lives only as long as the process that holds it.
package runs

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Registry is the slice of the operations registry the table needs: reserving the
// working-tree slot for one operation on behalf of a holder (the run id).
//
// §2.3 exemption — SDK seam: the consumer-side contract between the run table and
// execution/ops.Registry, with no core interface to embed.
type Registry interface {
	// Reserve takes the slot for the operation named name on behalf of holder; a held
	// slot fails with CodeUnavailable "busy: <holder> is running".
	Reserve(name, holder string) (interfaces.Reservation, error)
}

// State is where a run is in its life.
type State string

const (
	// StateRunning is a run whose operation has not returned yet.
	StateRunning State = "running"
	// StateSucceeded is a run whose operation returned nil.
	StateSucceeded State = "succeeded"
	// StateFailed is a run whose operation returned an error other than a cancel
	// (a timeout included).
	StateFailed State = "failed"
	// StateCancelled is a run stopped by Cancel or CancelAll.
	StateCancelled State = "cancelled"
)

// Table tracks the background runs of one front-end session. Build it with New.
type Table struct {
	// reg reserves the working-tree slot for each run.
	reg Registry
	// newID mints each run's id.
	newID func() string
	// onDone, when set, is called once as a run ends; the log paths it returns are
	// stored on the run, and its error is joined onto the run's own.
	onDone func(Snapshot) ([]string, error)
	// runs maps each run id to its run.
	runs map[string]*run
	// order is the run ids in start order.
	order []string
	// wg counts the run goroutines still executing.
	wg sync.WaitGroup
	// keep bounds how many finished runs the table remembers; <= 0 keeps every run.
	keep int
	// mu guards runs, order, closed, every run's mutable fields, and wg.Add.
	mu sync.Mutex
	// closed refuses new runs once CancelAll has begun the shutdown.
	closed bool
}

// New builds an empty table that reserves through reg, mints run ids with newID, and
// calls onDone (when non-nil) once per run as it ends — the front-end's hook to persist
// the run's step logs. It remembers at most keep finished runs, forgetting the oldest
// first (keep <= 0 remembers every run); a running run is never forgotten.
func New(
	reg Registry,
	newID func() string,
	onDone func(Snapshot) ([]string, error),
	keep int,
) *Table {
	return &Table{
		reg:    reg,
		newID:  newID,
		onDone: onDone,
		keep:   keep,
		runs:   map[string]*run{},
	}
}

// Start reserves the slot for the operation named name under a new run id and runs it
// in the background with input, returning the id at once. The run keeps ctx's values
// but not its cancellation: it outlives the call that started it and ends on its own,
// on its operation's timeout, or on Cancel / CancelAll. A reservation failure (unknown,
// excluded, or busy) is returned as is; a table that is shutting down fails with
// CodeUnavailable, and a run id the table already holds with CodeInternal.
func (t *Table) Start(ctx context.Context, name string, input any) (string, error) {
	id := t.newID()
	res, err := t.reg.Reserve(name, id)
	if err != nil {
		return "", err
	}
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &run{id: id, op: name, state: StateRunning, started: time.Now(), cancel: cancel}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		res.Release()
		return "", apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"operation %q not started: the server is shutting down", name,
		))
	}
	if _, dup := t.runs[id]; dup {
		t.mu.Unlock()
		cancel()
		res.Release()
		return "", apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"operation %q not started: run id %q is already in use", name, id,
		))
	}
	t.runs[id] = r
	t.order = append(t.order, id)
	t.wg.Add(1)
	t.mu.Unlock()
	go t.exec(rctx, r, res, input)
	return id, nil
}

// exec runs r's reserved operation to completion, then records its outcome: the log
// paths onDone returns are stored before the run leaves StateRunning, so a finished
// run's result always carries them, and an onDone error is joined onto the run's own
// (failing a run that succeeded). It then forgets finished runs beyond keep.
func (t *Table) exec(
	ctx context.Context,
	r *run,
	res interfaces.Reservation,
	input any,
) {
	defer t.wg.Done()
	defer r.cancel()
	err := res.Run(ctx, input, &r.obs)
	snap := r.snapshot()
	snap.State, snap.Err, snap.Ended = outcome(err), err, time.Now()
	var paths []string
	if t.onDone != nil {
		var derr error
		paths, derr = t.onDone(snap)
		if derr != nil {
			err = apperr.Join(err, derr)
			snap.State = outcome(err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r.state, r.err, r.ended, r.logPaths = snap.State, err, snap.Ended, paths
	t.trim()
}

// trim forgets the oldest finished runs until at most keep remain; t.mu must be held.
func (t *Table) trim() {
	if t.keep <= 0 {
		return
	}
	finished := 0
	for _, id := range t.order {
		if t.runs[id].state != StateRunning {
			finished++
		}
	}
	kept := t.order[:0]
	for _, id := range t.order {
		if finished > t.keep && t.runs[id].state != StateRunning {
			delete(t.runs, id)
			finished--
			continue
		}
		kept = append(kept, id)
	}
	t.order = kept
}

// outcome maps an operation's return to the state its run ends in.
func outcome(err error) State {
	switch {
	case err == nil:
		return StateSucceeded
	case apperr.Code(err) == apperr.CodeCanceled:
		return StateCancelled
	default:
		return StateFailed
	}
}

// Status returns run id's current snapshot (its steps so far while running). An
// unknown id fails with CodeNotFound.
func (t *Table) Status(id string) (Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.runs[id]
	if !ok {
		return Snapshot{}, notFound(id)
	}
	return r.snapshot(), nil
}

// Result returns run id's final snapshot. A run still running fails with
// CodeConflict; an unknown id with CodeNotFound.
func (t *Table) Result(id string) (Snapshot, error) {
	snap, err := t.Status(id)
	if err != nil {
		return Snapshot{}, err
	}
	if snap.State == StateRunning {
		return Snapshot{}, apperr.New(apperr.CodeConflict, fmt.Sprintf(
			"run %q is still running; poll its status", id,
		))
	}
	return snap, nil
}

// List returns every run's snapshot in start order.
func (t *Table) List() []Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Snapshot, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.runs[id].snapshot())
	}
	return out
}

// Cancel cancels run id's context; the run ends cancelled once its operation returns.
// Cancelling a finished run is a no-op; an unknown id fails with CodeNotFound.
func (t *Table) Cancel(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.runs[id]
	if !ok {
		return notFound(id)
	}
	r.cancel()
	return nil
}

// CancelAll cancels every run and refuses any later Start: the front-end's shutdown.
func (t *Table) CancelAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, r := range t.runs {
		r.cancel()
	}
}

// Wait blocks until every started run has ended and recorded its outcome.
func (t *Table) Wait() {
	t.wg.Wait()
}

// notFound is the error for a run id the table never issued.
func notFound(id string) error {
	return apperr.New(apperr.CodeNotFound, fmt.Sprintf("unknown run %q", id))
}

// cloneSnapshot copies s's slices so a caller cannot alias the table's state.
func cloneSnapshot(s Snapshot) Snapshot {
	s.Steps = append([]types.StepResult(nil), s.Steps...)
	s.LogPaths = slices.Clone(s.LogPaths)
	return s
}
