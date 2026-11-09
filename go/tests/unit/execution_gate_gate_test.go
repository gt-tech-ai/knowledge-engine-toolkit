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

	anyJob := mocks.NewMockAnyJob(gomock.NewController(t))
	anyJob.EXPECT().Meta().Return(types.JobMeta{Name: name}).AnyTimes()
	anyJob.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, interfaces.CommandRunner, string) (types.StepResults, error) {
			if onRun != nil {
				onRun()
			}

			return results, nil
		},
	).
		AnyTimes()

	return anyJob
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
	t.Parallel()

	var (
		lock  sync.Mutex
		order []string
	)

	record := func(name string) func() {
		return func() {
			lock.Lock()

			order = append(order, name)
			lock.Unlock()
		}
	}

	jobA := recordingJob(t, "job-a", passResults(textA), record(textA))
	jobB := recordingJob(t, "job-b", passResults(textB), record(textB))

	g := gate.New("ordering-gate").
		Serial("phase1", jobA).
		Serial("phase2", jobB)

	require.NoError(t, g.Run(context.Background(), gateNopRunner(t), textSlash))
	assert.Equal(t, []string{textA, textB}, order, "expected order [a, b]")
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
//   - When phase1 fails and StopOnFailure is set, Run returns an error and
//     phase2's job is never executed.
func TestGate_StopOnFailureSkipsLaterPhases(t *testing.T) {
	t.Parallel()

	var phase2Ran bool

	failing := recordingJob(t, textLint, failResults(textLint, "bad code"), nil)
	skipped := recordingJob(
		t,
		textTest,
		passResults(textTest),
		func() { phase2Ran = true },
	)

	currentGate := gate.New(textPreflight).
		Serial("phase1", failing).
		Serial("phase2", skipped).
		StopOnFailure()

	require.Error(
		t,
		currentGate.Run(context.Background(), gateNopRunner(t), textSlash),
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
//   - With two failing jobs in one parallel phase, Run returns a non-nil
//     error whose message contains both jobs' error strings.
func TestGate_AggregatesFailureErrors(t *testing.T) {
	t.Parallel()

	jobA := recordingJob(t, textA, failResults(textA, "err-alpha"), nil)
	jobB := recordingJob(t, textB, failResults(textB, "err-beta"), nil)

	g := gate.New("quality-gate").
		Parallel(textQuality, jobA, jobB)

	err := g.Run(context.Background(), gateNopRunner(t), textSlash)
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
//   - With two passing jobs in a parallel phase, Run returns nil.
func TestGate_NoErrorWhenAllPass(t *testing.T) {
	t.Parallel()

	jobA := recordingJob(t, textA, passResults(textA), nil)
	jobB := recordingJob(t, textB, passResults(textB), nil)

	g := gate.New("quality-gate").Parallel(textQuality, jobA, jobB)

	assert.NoError(
		t,
		g.Run(context.Background(), gateNopRunner(t), textSlash),
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
//   - A phase whose only result is StatusWarn produces a nil error from Run.
func TestGate_WarnDoesNotTriggerError(t *testing.T) {
	t.Parallel()

	anyJob := recordingJob(
		t, textSecurity,
		types.StepResults{{Name: textSecurity, Status: types.StatusWarn}},
		nil,
	)

	g := gate.New("security-gate").Serial("warnings", anyJob)

	assert.NoError(
		t,
		g.Run(context.Background(), gateNopRunner(t), textSlash),
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
	t.Parallel()

	jobA := recordingJob(t, textA, passResults(textA), nil)
	jobB := recordingJob(t, textB, passResults(textB), nil)

	g := gate.New("inspection-gate").
		Parallel("alpha", jobA).
		Serial("beta", jobB)

	phases := g.Phases()
	require.Len(t, phases, wantPhasesLen)
	assert.Equal(t, "alpha", phases[0].Name)
	assert.Equal(t, "beta", phases[1].Name)
}

// TestGate_WithObserver_ReceivesCallbacks tests that an observer registered via
// WithObserver receives phase and gate completion callbacks during a run.
//
// Why this test is important:
//   - The observer is the only channel through which the CLI/CI renderer learns
//     what happened; if Run silently skipped these callbacks the user would
//     see no progress lines or summary at all, even on a successful run.
//
// What it tests:
//   - After Run, the observer received at least one phase-complete callback
//     and exactly one gate-complete callback carrying the gate's name.
func TestGate_WithObserver_ReceivesCallbacks(t *testing.T) {
	t.Parallel()

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

	j := recordingJob(t, textLint, passResults(textLint), nil)
	g := gate.New("observed-gate").
		Serial(textCheck, j).
		Observe(obs)

	require.NoError(t, g.Run(context.Background(), gateNopRunner(t), textSlash))
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
	t.Parallel()

	ctrl := gomock.NewController(t)
	obs := mocks.NewMockExecutionObserver(ctrl)
	// Step start/complete callbacks fire (interleaved, parallel) but are not part
	// of the asserted ordering — the original spy tracked only gate/phase events.
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	// The ordered gate/phase lifecycle, with the exact phase and job names.
	gomock.InOrder(
		obs.EXPECT().OnGateStart("ci", []string{textCheck, "compile"}),
		obs.EXPECT().OnPhaseStart(textCheck, []string{textLint, textVet}),
		obs.EXPECT().OnPhaseComplete(textCheck, gomock.Any(), gomock.Any()),
		obs.EXPECT().OnPhaseStart("compile", []string{textBuild}),
		obs.EXPECT().OnPhaseComplete("compile", gomock.Any(), gomock.Any()),
		obs.EXPECT().OnGateComplete("ci", gomock.Any(), gomock.Any()),
	)

	lint := recordingJob(t, textLint, passResults(textLint), nil)
	vet := recordingJob(t, textVet, passResults(textVet), nil)
	build := recordingJob(t, textBuild, passResults(textBuild), nil)

	g := gate.New("ci").
		Parallel(textCheck, lint, vet).
		Serial("compile", build).
		Observe(obs)

	require.NoError(t, g.Run(context.Background(), gateNopRunner(t), textSlash))
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
	t.Parallel()

	ctrl := gomock.NewController(t)
	obs := mocks.NewMockExecutionObserver(ctrl)
	obs.EXPECT().OnGateStart(textEmpty, []string{"nothing"}).Times(1)
	obs.EXPECT().OnPhaseStart("nothing", gomock.Len(0)).Times(1)
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	g := gate.New(textEmpty).
		Serial("nothing").
		Observe(obs)

	require.NoError(t, g.Run(context.Background(), gateNopRunner(t), textSlash))
}
