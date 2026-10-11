package unit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// --- mock helpers ---

// gateNopRunner returns a MockCommandRunner with no expectations: the gate tests use
// jobs whose Execute ignores the runner, so it is never actually invoked.
func gateNopRunner(t *testing.T) interfaces.CommandRunner {
	t.Helper()
	return mocks.NewMockCommandRunner(gomock.NewController(t))
}

// recordingJob returns a MockAnyJob named name whose Execute invokes onRun (if any)
// then returns results — reproducing the old stub job's optional run-callback, used
// to observe execution order and short-circuiting.
func recordingJob(
	t *testing.T,
	name string,
	results types.StepResults,
	onRun func(),
) interfaces.AnyJob {
	t.Helper()
	j := mocks.NewMockAnyJob(gomock.NewController(t))
	j.EXPECT().Meta().Return(types.JobMeta{Name: name}).AnyTimes()
	j.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, interfaces.CommandRunner, string) (types.StepResults, error) {
			if onRun != nil {
				onRun()
			}
			return results, nil
		},
	).
		AnyTimes()
	return j
}

// passResults / failResults / warnResults build a single-step result set.
func passResults(name string) types.StepResults {
	return types.StepResults{{Name: name, Status: types.StatusPass}}
}

func failResults(name, msg string) types.StepResults {
	return types.StepResults{{Name: name, Status: types.StatusFail, Error: msg}}
}

// --- tests ---

// TestGate_PhaseOrdering tests that phases execute in the order they were added
// to the gate.
//
// Why this test is important:
//   - Gates model ordered pipelines (e.g. lint before build before test); if
//     phases ran out of order, later phases would observe stale or
//     unprepared state and the stop-on-failure contract would be meaningless.
//
// What it tests:
//   - With two serial phases added in sequence, the job in phase1 runs strictly
//     before the job in phase2 (observed via recorded execution order).
func TestGate_PhaseOrdering(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) func() {
		return func() {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
		}
	}

	jobA := recordingJob(t, "job-a", passResults("a"), record("a"))
	jobB := recordingJob(t, "job-b", passResults("b"), record("b"))

	g := gate.NewGate("ordering-gate").
		Serial("phase1", jobA).
		Serial("phase2", jobB).
		Build()

	require.NoError(t, gate.RunGate(context.Background(), gateNopRunner(t), "/", g))
	assert.Equal(t, []string{"a", "b"}, order, "expected order [a, b]")
}

// TestGate_StopOnFailureSkipsLaterPhases tests that a StopOnFailure gate halts
// after the first failing phase and never runs subsequent phases.
//
// Why this test is important:
//   - This is the fail-fast contract behind a preflight gate: once linting
//     fails there is no point running the test phase. If later phases still ran
//     the developer would wait for irrelevant work and the gate would mask the
//     real first failure under a wall of downstream noise.
//
// What it tests:
//   - When phase1 fails and StopOnFailure is set, RunGate returns an error and
//     phase2's job is never executed.
func TestGate_StopOnFailureSkipsLaterPhases(t *testing.T) {
	var phase2Ran bool

	failing := recordingJob(t, "lint", failResults("lint", "bad code"), nil)
	skipped := recordingJob(t, "test", passResults("test"), func() { phase2Ran = true })

	g := gate.NewGate("preflight").
		Serial("phase1", failing).
		Serial("phase2", skipped).
		StopOnFailure().
		Build()

	require.Error(
		t,
		gate.RunGate(context.Background(), gateNopRunner(t), "/", g),
		"expected error from failed phase",
	)
	assert.False(t, phase2Ran, "phase2 should have been skipped after phase1 failure")
}

// TestGate_AggregatesFailureErrors tests that when multiple jobs in a phase
// fail, the returned error contains every failure's message, not just the first.
//
// Why this test is important:
//   - A parallel phase can surface several independent failures at once (e.g.
//     Go, Python, and TS lint all failing). Collapsing them to a single error
//     would hide real problems and force the developer into repeated re-runs to
//     discover each one. The aggregated error is what the CLI prints as the
//     grouped failure block.
//
// What it tests:
//   - With two failing jobs in one parallel phase, RunGate returns a non-nil
//     error whose message contains both jobs' error strings.
func TestGate_AggregatesFailureErrors(t *testing.T) {
	jobA := recordingJob(t, "a", failResults("a", "err-alpha"), nil)
	jobB := recordingJob(t, "b", failResults("b", "err-beta"), nil)

	g := gate.NewGate("quality-gate").
		Parallel("quality", jobA, jobB).
		Build()

	err := gate.RunGate(context.Background(), gateNopRunner(t), "/", g)
	require.Error(t, err, "expected error for two failed jobs")
	msg := err.Error()
	assert.Contains(
		t,
		msg,
		"err-alpha",
		"error should aggregate both failure messages, got: %v",
		msg,
	)
	assert.Contains(
		t,
		msg,
		"err-beta",
		"error should aggregate both failure messages, got: %v",
		msg,
	)
}

// TestGate_NoErrorWhenAllPass tests that a gate whose every step passes returns
// a nil error.
//
// Why this test is important:
//   - This is the success path that gates a clean exit code (0) for `search
//     preflight`/CI. A false-positive error here would block merges and break
//     trust in the gate even when nothing is actually wrong.
//
// What it tests:
//   - With two passing jobs in a parallel phase, RunGate returns nil.
func TestGate_NoErrorWhenAllPass(t *testing.T) {
	jobA := recordingJob(t, "a", passResults("a"), nil)
	jobB := recordingJob(t, "b", passResults("b"), nil)

	g := gate.NewGate("quality-gate").Parallel("quality", jobA, jobB).Build()

	assert.NoError(
		t,
		gate.RunGate(context.Background(), gateNopRunner(t), "/", g),
		"expected no error when all steps pass",
	)
}

// TestGate_WarnDoesNotTriggerError tests that Warn-status results are
// non-blocking: they do not cause the gate to return an error.
//
// Why this test is important:
//   - Advisory phases (e.g. security scanning) emit warnings that should inform
//     the developer without failing the build. If Warn were treated like Fail,
//     advisory checks would block merges, defeating the distinction between
//     blocking and non-blocking signals the gate is built to express.
//
// What it tests:
//   - A phase whose only result is StatusWarn produces a nil error from RunGate.
func TestGate_WarnDoesNotTriggerError(t *testing.T) {
	j := recordingJob(
		t, "security",
		types.StepResults{{Name: "security", Status: types.StatusWarn}},
		nil,
	)

	g := gate.NewGate("security-gate").Serial("warnings", j).Build()

	assert.NoError(
		t,
		gate.RunGate(context.Background(), gateNopRunner(t), "/", g),
		"Warn results should not produce an error",
	)
}

// TestGate_Phases_ReturnsOrderedPhases tests that Gate.Phases exposes the
// configured phases in their declared order, regardless of parallel/serial mode.
//
// Why this test is important:
//   - Phases is the inspection seam used by the UI and tests to render and
//     reason about a gate before it runs. If it dropped phases or reordered
//     them, the displayed plan would not match what actually executes.
//
// What it tests:
//   - After adding a parallel "alpha" phase then a serial "beta" phase, Phases
//     returns exactly two phases named "alpha" then "beta" in that order.
func TestGate_Phases_ReturnsOrderedPhases(t *testing.T) {
	jobA := recordingJob(t, "a", passResults("a"), nil)
	jobB := recordingJob(t, "b", passResults("b"), nil)

	g := gate.NewGate("inspection-gate").
		Parallel("alpha", jobA).
		Serial("beta", jobB).
		Build()

	phases := g.Phases()
	require.Len(t, phases, 2)
	assert.Equal(t, "alpha", phases[0].Name)
	assert.Equal(t, "beta", phases[1].Name)
}

// TestGate_WithObserver_ReceivesCallbacks tests that an observer registered via
// WithObserver receives phase and gate completion callbacks during a run.
//
// Why this test is important:
//   - The observer is the only channel through which the CLI/CI renderer learns
//     what happened; if RunGate silently skipped these callbacks the user would
//     see no progress lines or summary at all, even on a successful run.
//
// What it tests:
//   - After RunGate, the observer received at least one phase-complete callback
//     and exactly one gate-complete callback carrying the gate's name.
func TestGate_WithObserver_ReceivesCallbacks(t *testing.T) {
	ctrl := gomock.NewController(t)
	obs := mocks.NewMockExecutionObserver(ctrl)
	// The MinTimes/Times expectations ARE the assertions (verified at ctrl finish):
	// at least one phase completes, and the gate completes exactly once with its name.
	obs.EXPECT().OnGateStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).MinTimes(1)
	obs.EXPECT().OnGateComplete("observed-gate", gomock.Any(), gomock.Any()).Times(1)

	j := recordingJob(t, "lint", passResults("lint"), nil)
	g := gate.NewGate("observed-gate").
		Serial("check", j).
		WithObserver(obs).
		Build()

	require.NoError(t, gate.RunGate(context.Background(), gateNopRunner(t), "/", g))
}

// TestGate_FiresStartEvents tests that the gate emits the full lifecycle of
// start/complete events in the correct order, with the correct phase and job
// names attached.
//
// Why this test is important:
//   - The renderer draws "now running" lines from the start events and the
//     final summary from the complete events; their interleaving (gate-start
//     first, each phase-start before its phase-complete, gate-complete last) is
//     what makes the live output coherent. A misordered or missing start event
//     produces blank or duplicated lines and wrong job attribution.
//
// What it tests:
//   - OnGateStart fires once carrying the phase names in order ([check compile]).
//   - OnPhaseStart fires per phase carrying that phase's job names ([lint vet],
//     then [build]).
//   - The full event sequence is exactly gate-start, phase-start/complete per
//     phase, gate-complete, in that order (enforced by gomock.InOrder).
func TestGate_FiresStartEvents(t *testing.T) {
	ctrl := gomock.NewController(t)
	obs := mocks.NewMockExecutionObserver(ctrl)
	// Step start/complete callbacks fire (interleaved, parallel) but are not part
	// of the asserted ordering — the original spy tracked only gate/phase events.
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	// The ordered gate/phase lifecycle, with the exact phase and job names.
	gomock.InOrder(
		obs.EXPECT().OnGateStart("ci", []string{"check", "compile"}),
		obs.EXPECT().OnPhaseStart("check", []string{"lint", "vet"}),
		obs.EXPECT().OnPhaseComplete("check", gomock.Any(), gomock.Any()),
		obs.EXPECT().OnPhaseStart("compile", []string{"build"}),
		obs.EXPECT().OnPhaseComplete("compile", gomock.Any(), gomock.Any()),
		obs.EXPECT().OnGateComplete("ci", gomock.Any(), gomock.Any()),
	)

	lint := recordingJob(t, "lint", passResults("lint"), nil)
	vet := recordingJob(t, "vet", passResults("vet"), nil)
	build := recordingJob(t, "build", passResults("build"), nil)

	g := gate.NewGate("ci").
		Parallel("check", lint, vet).
		Serial("compile", build).
		WithObserver(obs).
		Build()

	require.NoError(t, gate.RunGate(context.Background(), gateNopRunner(t), "/", g))
}

// TestGate_EmptyPhase_FiresStartCleanly tests that a phase with no jobs still
// fires OnPhaseStart (with an empty job list) and does not panic.
//
// Why this test is important:
//   - Phases can legitimately be empty when language flags filter every job out
//     (e.g. a TS-only change leaves the Go phase with zero jobs). The renderer
//     still needs the phase-start event to account for the phase, and an
//     index-out-of-range panic on the empty job slice would crash the whole gate.
//
// What it tests:
//   - A serial phase with zero jobs yields exactly one gate-start and one
//     phase-start whose job-name list is empty, with no error or panic.
func TestGate_EmptyPhase_FiresStartCleanly(t *testing.T) {
	ctrl := gomock.NewController(t)
	obs := mocks.NewMockExecutionObserver(ctrl)
	obs.EXPECT().OnGateStart("empty", []string{"nothing"}).Times(1)
	obs.EXPECT().OnPhaseStart("nothing", gomock.Len(0)).Times(1)
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	g := gate.NewGate("empty").
		Serial("nothing").
		WithObserver(obs).
		Build()

	require.NoError(t, gate.RunGate(context.Background(), gateNopRunner(t), "/", g))
}
