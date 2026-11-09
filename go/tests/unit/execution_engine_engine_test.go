package unit_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// engineNopRunner returns a CommandRunner mock with no programmed expectations. Every
// job exercised here ignores the runner — the mock jobs return canned results and
// the fan-out job fans out through engine.Pool.FanOut with its own closure — so any
// runner call would be a real regression, which the bare mock surfaces (unexpected
// call) rather than silently absorbing.
func engineNopRunner(t *testing.T) interfaces.CommandRunner {
	t.Helper()

	return mocks.NewMockCommandRunner(gomock.NewController(t))
}

// capturingObserver returns a MockExecutionObserver whose every method is an AnyTimes
// no-op, except OnStepComplete which forwards to onStep (when non-nil) so a test can
// capture the per-step callbacks it asserts on. Each method is registered exactly once
// to avoid one expectation shadowing another.
func capturingObserver(
	t *testing.T,
	onStep func(types.StepResult),
) *mocks.MockExecutionObserver {
	t.Helper()

	obs := mocks.NewMockExecutionObserver(gomock.NewController(t))
	obs.EXPECT().OnGateStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	if onStep != nil {
		obs.EXPECT().OnStepComplete(gomock.Any()).
			Do(func(r *types.StepResult) { onStep(*r) }).AnyTimes()
	} else {
		obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	}

	return obs
}

// newFanOutJob returns a MockAnyJob named "fanout" whose Execute fans out over items
// via engine.Pool.FanOut; the unit named blockOn waits on release before completing, and
// extra (if set) is appended after the fan-out (mirrors a service that adds e.g. a
// coverage step). It reproduces the former hand-rolled fan-out job.
func newFanOutJob(
	t *testing.T,
	items []string,
	blockOn string,
	release chan struct{},
	extra *types.StepResult,
) interfaces.AnyJob {
	t.Helper()

	anyJob := mocks.NewMockAnyJob(gomock.NewController(t))
	anyJob.EXPECT().
		Meta().
		Return(fixtures.Build(func(row *types.JobMeta) {
			row.Name = "fanout"
		})).
		AnyTimes()

	unit := fanOutUnit(blockOn, release)

	anyJob.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ interfaces.CommandRunner,
			_ string,
		) (types.StepResults, error) {
			results := engine.Pool(len(items)).FanOut(ctx, items, unit)
			if extra != nil {
				results = append(results, *extra)
			}

			return results, nil
		},
	).
		AnyTimes()

	return anyJob
}

// fanOutUnit returns a unit that passes under its item's name; the unit named blockOn
// first waits on release when release is set.
func fanOutUnit(
	blockOn string,
	release chan struct{},
) func(context.Context, string) types.StepResult {
	return func(_ context.Context, item string) types.StepResult {
		if item == blockOn && release != nil {
			<-release
		}

		return passedStep(item)
	}
}

// passedStep builds a passing step result with only its name set.
func passedStep(name string) types.StepResult {
	return fixtures.Build(func(row *types.StepResult) {
		row.Name = name
		row.Status = types.StatusPass
	})
}

// statusCounts tallies results by status.
func statusCounts(results types.StepResults) map[types.StepStatus]int {
	counts := map[types.StepStatus]int{}
	for index := range results {
		counts[results[index].Status]++
	}

	return counts
}

// isPanickedStep reports whether a result is the failed result of a panicking unit;
// its name may be empty depending on where the panic was recovered.
func isPanickedStep(step *types.StepResult) bool {
	return step.Status == types.StatusFail &&
		(step.Name == textPanicMe || step.Name == "")
}

// --- FanOut tests ---

// TestFanOut_AllUnitsCollected tests that FanOut returns one result per input
// item, with a failing unit recorded as Fail and the rest as Pass.
//
// Why this test is important:
//   - FanOut is the batch substrate behind every CLI gate; a single failing
//     unit must not drop or cancel its siblings. If results were lost or a
//     failure aborted the group, the gate would report an inaccurate count and
//     could hide passing or failing work.
//
// What it tests:
//   - len(results) equals len(items), with exactly one Fail and the remainder
//     Pass when one unit returns a failure.
func TestFanOut_AllUnitsCollected(t *testing.T) {
	t.Parallel()

	items := []int{0, 1, items, itemsCollected, itemsUnitsCollected}
	results := engine.Pool(2).
		FanOut(
			context.Background(), items,
			func(_ context.Context, item int) types.StepResult {
				return allUnitsStep(item)
			})

	require.Len(t, results, len(items))
	assert.Equal(
		t,
		map[types.StepStatus]int{types.StatusFail: 1, types.StatusPass: wantPassed},
		statusCounts(results),
		"one unit fails and the rest pass",
	)
}

// allUnitsStep fails item 2 and passes every other item.
func allUnitsStep(item int) types.StepResult {
	name := fmt.Sprintf("item-%d", item)
	if item != item2 {
		return passedStep(name)
	}

	return fixtures.Build(func(row *types.StepResult) {
		row.Name = name
		row.Status = types.StatusFail
		row.Error = "injected failure"
	})
}

// TestFanOut_BoundedConcurrency tests that FanOut never runs more units in
// parallel than the configured worker count.
//
// Why this test is important:
//   - The worker bound is the only guard against fan-out work (test runs,
//     builds, network calls) overwhelming the machine or a rate-limited
//     service. If the semaphore leaked slots, concurrency would be unbounded
//     and the supposed limit would be a lie.
//
// What it tests:
//   - Across many items the observed peak number of concurrently-running units
//     never exceeds maxWorkers.
func TestFanOut_BoundedConcurrency(t *testing.T) {
	t.Parallel()

	const (
		numItems   = 10
		maxWorkers = 3
	)

	var peak, current int64

	items := make([]int, numItems)

	engine.Pool(maxWorkers).
		FanOut(
			context.Background(), items,
			func(_ context.Context, _ int) types.StepResult {
				count := atomic.AddInt64(&current, 1)
				defer atomic.AddInt64(&current, -1)

				// track peak
				for {
					p := atomic.LoadInt64(&peak)
					if count <= p || atomic.CompareAndSwapInt64(&peak, p, count) {
						break
					}
				}

				time.Sleep(5 * time.Millisecond) // hold slot briefly

				return fixtures.Build(func(row *types.StepResult) {
					row.Status = types.StatusPass
				})
			})

	assert.LessOrEqual(
		t,
		peak, int64(maxWorkers),
		"peak concurrency %d exceeded maxWorkers %d",
		peak,
		maxWorkers,
	)
}

// TestFanOut_PanicConvertsToFail tests that a panic in one unit is recovered
// and surfaced as a Fail result carrying a stack trace, not a process crash.
//
// Why this test is important:
//   - One misbehaving unit (nil deref, bad cast) must not take down the whole
//     CLI run and the other units' results with it. Recovering the panic into a
//     Fail with diagnostics is what keeps the batch resilient and debuggable.
//
// What it tests:
//   - The panicking item yields a StatusFail result whose Detail is non-empty
//     (the captured stack), while sibling units still complete.
func TestFanOut_PanicConvertsToFail(t *testing.T) {
	t.Parallel()

	items := []string{textOk, textPanicMe, "ok2"}
	results := engine.Pool(2).
		FanOut(
			context.Background(), items,
			func(_ context.Context, item string) types.StepResult {
				if item == textPanicMe {
					panic("simulated panic")
				}

				return passedStep(item)
			})

	require.Len(t, results, wantResultsLenFail)

	index := slices.IndexFunc(
		results,
		func(r types.StepResult) bool { return isPanickedStep(&r) },
	)
	require.GreaterOrEqual(t, index, 0, "no failed result found for the panicking item")
	assert.NotEmpty(
		t,
		results[index].Detail,
		"panic result should have Detail (stack trace)",
	)
}

// TestFanOut_CtxCancelStopsFeeding tests that cancelling the context bails out
// units that have not yet acquired a worker slot, while still returning one
// result per item.
//
// Why this test is important:
//   - On cancellation (Ctrl+C, timeout) queued work should stop promptly rather
//     than grinding through every remaining item. The result slice must still
//     be complete so the gate can account for every unit — cancelled ones as
//     Fail — instead of seeing a short, ambiguous result set.
//
// What it tests:
//   - After cancelling while one unit holds the lone worker slot, the call
//     returns len(items) results and at least one is a StatusFail (cancelled).
func TestFanOut_CtxCancelStopsFeeding(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	released := make(chan struct{})
	items := make([]int, makeInputFeeding)

	results := make(chan types.StepResults, 1)
	go func() {
		// numWorkers=1: only one item runs at a time, others queue at semaphore
		results <- engine.Pool(1).FanOut(ctx, items, func(
			_ context.Context,
			item int,
		) types.StepResult {
			if item == 0 {
				close(started) // signal first item started
				<-released     // hold semaphore until test releases
			}

			return passedStep(strconv.Itoa(item))
		})
	}()

	<-started       // first item holds the semaphore
	cancel()        // cancel ctx — goroutines waiting at semaphore should bail
	close(released) // let first item finish

	stepResults := <-results
	require.Len(t, stepResults, len(items), "expected one result per item")

	// At least the items that didn't get a semaphore slot should be Fail (cancelled).
	// Item 0 ran to completion (StatusPass); others should be Fail.
	// With numWorkers=1, items 1-7 cannot proceed after cancellation.
	assert.GreaterOrEqual(
		t,
		statusCounts(stepResults)[types.StatusFail],
		1,
		"expected at least 1 cancelled (Fail) result",
	)
}

// --- RunJobGroup smoke test ---

// TestRunJobGroup_CollectsAllResults tests that RunJobGroup aggregates every
// job's results and forwards each one to OnStepComplete exactly once.
//
// Why this test is important:
//   - RunJobGroup is the per-phase fan-out: a lost result or a missed step
//     callback means the final table and CI counts under-report work, and a
//     failing job could silently disappear. Phase aggregation is deliberately
//     the gate's job, so RunJobGroup must NOT also fire OnPhaseComplete.
//
// What it tests:
//   - Two jobs (one Pass, one Fail) produce two collected results and exactly
//     two OnStepComplete callbacks.
func TestRunJobGroup_CollectsAllResults(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	// One Pass job and one Fail job, each run exactly once by RunJobGroup; Times(1)
	// pins the "each job invoked once" half of the contract the test asserts.
	jobA := mocks.NewMockAnyJob(ctrl)
	jobA.EXPECT().
		Meta().
		Return(fixtures.Build(func(row *types.JobMeta) {
			row.Name = "job-a"
		})).
		AnyTimes()
	jobA.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{fixtures.Build(func(row *types.StepResult) {
			row.Name = textA
			row.Status = types.StatusPass
		})}, nil).
		Times(1)

	jobB := mocks.NewMockAnyJob(ctrl)
	jobB.EXPECT().
		Meta().
		Return(fixtures.Build(func(row *types.JobMeta) {
			row.Name = "job-b"
		})).
		AnyTimes()
	jobB.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{fixtures.Build(func(row *types.StepResult) {
			row.Name = textB
			row.Status = types.StatusFail
			row.Error = "oops"
		})}, nil).
		Times(1)

	jobs := []interfaces.AnyJob{jobA, jobB}

	var steps []types.StepResult

	obs := capturingObserver(t, func(r types.StepResult) { steps = append(steps, r) })

	results, err := engine.RunJobGroup(
		context.Background(),
		engineNopRunner(t),
		textRoot,
		jobs,
		obs,
	)
	require.NoError(t, err)
	require.Len(t, results, wantResultsLenResults)
	// RunJobGroup forwards each result to OnStepComplete; phase aggregation is
	// the gate's responsibility, so RunJobGroup no longer fires OnPhaseComplete.
	assert.Len(t, steps, wantStepsLen, "expected 2 OnStepComplete callbacks")
}

// TestObserverStartEvents tests the observer's lifecycle start hooks:
// OnGateStart, OnPhaseStart, and OnStepStart.
//
// Why this test is important:
//   - The start events drive the live "==>" progress lines the UI prints as a
//     run begins; if they were dropped or mis-shaped (e.g. losing the phase
//     list), the user would see no progress until completion. NopObserver must
//     also treat them as no-ops so unobserved runs don't panic.
//
// What it tests:
//   - A recording observer captures each start event with its arguments (gate
//     name + phase list, phase name, step name), and NopObserver accepts the
//     same calls without panicking.
func TestObserverStartEvents(t *testing.T) {
	t.Parallel()

	var nop interfaces.ExecutionObserver = engine.NopObserver{}

	// Must not panic.
	nop.OnGateStart(textGate, []string{textPhaseA})
	nop.OnPhaseStart(textPhaseA, []string{textJob1})
	nop.OnStepStart(textJob1, textPhaseA)

	var (
		gateStarts, phaseStarts, stepStarts []string
		gateStartPhases                     [][]string
	)

	obs := mocks.NewMockExecutionObserver(gomock.NewController(t))
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	testObserverStartEventsChecksGateAndStepStarts(
		t,
		gateStarts,
		phaseStarts,
		stepStarts,
		gateStartPhases,
		obs,
	)
}

// testObserverStartEventsChecksGateAndStepStarts continues TestObserverStartEvents:
// checks gate and step starts.
func testObserverStartEventsChecksGateAndStepStarts(
	t *testing.T,
	gateStarts, phaseStarts, stepStarts []string,
	gateStartPhases [][]string,
	obs *mocks.MockExecutionObserver,
) {
	t.Helper()

	obs.EXPECT().
		OnGateStart(gomock.Any(), gomock.Any()).
		Do(func(name string, phases []string) {
			gateStarts = append(gateStarts, name)
			gateStartPhases = append(gateStartPhases, phases)
		}).
		AnyTimes()
	obs.EXPECT().
		OnPhaseStart(gomock.Any(), gomock.Any()).
		Do(func(name string, _ []string) {
			phaseStarts = append(phaseStarts, name)
		}).
		AnyTimes()
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).Do(func(name, _ string) {
		stepStarts = append(stepStarts, name)
	}).AnyTimes()

	var o interfaces.ExecutionObserver = obs

	o.OnGateStart(textGate, []string{textPhaseA, "phase-b"})
	o.OnPhaseStart(textPhaseA, []string{textJob1})
	o.OnStepStart(textJob1, textPhaseA)

	require.Len(t, gateStarts, 1, "OnGateStart not recorded: %v", gateStarts)
	require.Equal(t, textGate, gateStarts[0], "OnGateStart not recorded: %v", gateStarts)
	require.Len(
		t,
		gateStartPhases,
		1,
		"OnGateStart phases not recorded: %v",
		gateStartPhases,
	)
	require.Len(
		t,
		gateStartPhases[0],
		wantGateStartPhasesLen,
		"OnGateStart phases not recorded: %v",
		gateStartPhases,
	)
	require.Len(t, phaseStarts, 1, "OnPhaseStart not recorded: %v", phaseStarts)
	require.Equal(
		t,
		textPhaseA,
		phaseStarts[0],
		"OnPhaseStart not recorded: %v",
		phaseStarts,
	)
	require.Len(t, stepStarts, 1, "OnStepStart not recorded: %v", stepStarts)
	require.Equal(t, textJob1, stepStarts[0], "OnStepStart not recorded: %v", stepStarts)
}

// --- Live per-unit emission from FanOut ---

// TestRunJobGroup_FanOutEmitsLivePerUnit tests that a fan-out unit's
// OnStepComplete fires live — while a sibling unit is still running — and that
// each unit is delivered exactly once.
//
// Why this test is important:
//   - A long fan-out (e.g. many Go modules under test) must show progress as
//     each unit finishes; if events were batched until the job returned, the UI
//     would go silent for the whole run. The exactly-once guarantee is what
//     prevents the same unit being double-counted between FanOut's live emit
//     and RunJobGroup's trailing flush.
//
// What it tests:
//   - The fast unit's OnStepComplete is observed before the slow unit is
//     released, and after completion each unit's callback fired exactly once.
func TestRunJobGroup_FanOutEmitsLivePerUnit(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	testRunJobGroupFanOutEmitsLivePerUnitRuns(t, release)
}

// testRunJobGroupFanOutEmitsLivePerUnitRuns continues
// TestRunJobGroup_FanOutEmitsLivePerUnit: runs.
func testRunJobGroupFanOutEmitsLivePerUnitRuns(t *testing.T, release chan struct{}) {
	t.Helper()

	var (
		lock  sync.Mutex
		names []string
	)

	seen := make(chan struct{})

	var seenOnce bool

	obs := capturingObserver(t, func(r types.StepResult) {
		lock.Lock()
		defer lock.Unlock()

		names = append(names, r.Name)
		if r.Name == textFast && !seenOnce {
			seenOnce = true

			close(seen)
		}
	})

	job := newFanOutJob(t, []string{textFast, textSlow}, textSlow, release, nil)
	runner := engineNopRunner(t)

	done := make(chan struct{})

	go func() {
		_, _ = engine.RunJobGroup(
			context.Background(), runner, textSlash,
			[]interfaces.AnyJob{job}, obs,
		)

		close(done)
	}()

	testRunJobGroupFanOutEmitsLivePerUnitChecksLiveUnits(
		t,
		release,
		&lock,
		&names,
		seen,
		done,
	)
}

// testRunJobGroupFanOutEmitsLivePerUnitChecksLiveUnits continues
// testRunJobGroupFanOutEmitsLivePerUnitRuns: checks live units.
func testRunJobGroupFanOutEmitsLivePerUnitChecksLiveUnits(
	t *testing.T,
	release chan struct{},
	lock *sync.Mutex,
	names *[]string,
	seen, done chan struct{},
) {
	t.Helper()

	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		require.Fail(
			t,
			"fast unit's OnStepComplete not observed live (batched at job end?)",
		)
	}

	close(release)
	<-done

	lock.Lock()

	statusByName := map[string]int{}
	for _, n := range *names {
		statusByName[n]++
	}

	lock.Unlock()
	require.Equal(
		t,
		1,
		statusByName[textFast],
		"expected each unit delivered exactly once, got %v",
		statusByName,
	)
	require.Equal(
		t,
		1,
		statusByName[textSlow],
		"expected each unit delivered exactly once, got %v",
		statusByName,
	)
}

// TestRunJobGroup_FanOutPlusExtraStep_NoDoubleFire tests that a step a job
// appends after its fan-out is still delivered, while the fan-out units that
// already emitted live are not delivered a second time.
//
// Why this test is important:
//   - Some jobs add a trailing step (e.g. a coverage summary) after fanning
//     out. RunJobGroup must flush exactly those trailing extras — using the
//     emitter's delivered count — without re-firing the fan-out units. Getting
//     the boundary wrong would either drop the extra step or double-count the
//     fan-out units in the final table and CI totals.
//
// What it tests:
//   - The aggregate result set has all three results, and each of the two
//     fan-out units and the appended step is delivered exactly once.
func TestRunJobGroup_FanOutPlusExtraStep_NoDoubleFire(t *testing.T) {
	t.Parallel()

	extra := fixtures.Build(func(row *types.StepResult) {
		row.Name = "coverage"
		row.Status = types.StatusPass
	})

	var (
		lock  sync.Mutex
		names []string
	)

	obs := capturingObserver(t, func(r types.StepResult) {
		lock.Lock()

		names = append(names, r.Name)
		lock.Unlock()
	})

	job := newFanOutJob(t, []string{textA, textB}, "", nil, &extra)

	all, _ := engine.RunJobGroup(
		context.Background(), engineNopRunner(t), textSlash,
		[]interfaces.AnyJob{job}, obs,
	)

	require.Len(t, all, wantAllLen, "want 3 aggregate results")
	lock.Lock()

	statusByName := map[string]int{}
	for _, n := range names {
		statusByName[n]++
	}

	lock.Unlock()
	require.Equal(t, 1, statusByName[textA], textExpectedEachStepDelivered, statusByName)
	require.Equal(t, 1, statusByName[textB], textExpectedEachStepDelivered, statusByName)
	require.Equal(
		t,
		1,
		statusByName["coverage"],
		textExpectedEachStepDelivered,
		statusByName,
	)
}

// --- engine.Step tests ---

// TestStep_PassOnSuccess tests that a Step whose function returns nil yields
// a Pass result carrying the step's name and group.
//
// Why this test is important:
//   - Step is the simple function-to-result wrapper CLI commands rely on.
//     The name and group are how a result is identified and grouped in the
//     output; losing them or mis-classifying a success would scramble the
//     run summary.
//
// What it tests:
//   - A nil-returning step produces StatusPass with the supplied name "lint"
//     and group "quality".
func TestStep_PassOnSuccess(t *testing.T) {
	t.Parallel()

	step := engine.Step{
		Name:     textLint,
		Group:    textQuality,
		Run:      func(_ context.Context) error { return nil },
		WarnOnly: false,
	}
	result := step.Execute(context.Background())
	assert.Equal(t, types.StatusPass, result.Status)
	assert.Equal(t, textLint, result.Name)
	assert.Equal(t, textQuality, result.Group)
}

// TestStep_FailOnError tests that a Step whose function returns an error
// yields a Fail result with a non-empty error message.
//
// Why this test is important:
//   - A step failure must surface as StatusFail with a message so the gate
//     fails the run and the user sees why. If an error were swallowed into a
//     Pass or produced an empty message, broken checks would pass silently.
//
// What it tests:
//   - An erroring step produces StatusFail and a non-empty Error string.
func TestStep_FailOnError(t *testing.T) {
	t.Parallel()

	step := engine.Step{
		Name:  "typecheck",
		Group: textQuality,
		Run: func(_ context.Context) error {
			return apperr.Sentinel("type error")
		},
		WarnOnly: false,
	}
	result := step.Execute(context.Background())
	assert.Equal(t, types.StatusFail, result.Status)
	assert.NotEmpty(t, result.Error, "expected non-empty error message")
}

// TestStep_WarnOnly_DowngradesFailure tests that WarnOnly
// turns a failing step's result into a Warn instead of a Fail.
//
// Why this test is important:
//   - Advisory checks (e.g. shellcheck on infra) should flag issues without
//     blocking the gate. The warn-only downgrade is what keeps a non-blocking
//     check from failing the whole run; if it didn't apply, advisory tools
//     would gate merges.
//
// What it tests:
//   - A warn-only step whose function errors produces StatusWarn, not
//     StatusFail.
func TestStep_WarnOnly_DowngradesFailure(t *testing.T) {
	t.Parallel()

	step := engine.Step{
		Name:     textShellcheck,
		Group:    textInfra,
		Run:      func(_ context.Context) error { return apperr.Sentinel("shell issue") },
		WarnOnly: true,
	}
	result := step.Execute(context.Background())
	assert.Equal(t, types.StatusWarn, result.Status)
}

// TestStep_WarnOnly_PreservesPass tests that WarnOnly only
// downgrades failures and leaves a passing result a Pass.
//
// Why this test is important:
//   - The downgrade must be scoped to failures. If warn-only also rewrote
//     successes, a passing advisory check would be reported as a warning,
//     cluttering output with false warnings and eroding trust in the signal.
//
// What it tests:
//   - A warn-only step whose function succeeds still produces StatusPass.
func TestStep_WarnOnly_PreservesPass(t *testing.T) {
	t.Parallel()

	step := engine.Step{
		Name:     "ok-step",
		Group:    textTest,
		Run:      func(_ context.Context) error { return nil },
		WarnOnly: true,
	}
	result := step.Execute(context.Background())
	assert.Equal(
		t,
		types.StatusPass,
		result.Status,
		"expected StatusPass (not downgraded)",
	)
}

// TestStepsSerial_RunsInOrder tests that Steps.Serial executes steps one at a
// time in input order and returns one result per step.
//
// Why this test is important:
//   - Steps.Serial is for order-dependent sequences (e.g. a resource prune where
//     compose-down must precede image removal). If it ran out of order or dropped a
//     step, the dependent cleanup would act on stale state. This pins the ordering
//     guarantee — and, because the steps append to a shared slice without a lock,
//     that it runs strictly serially.
//
// What it tests:
//   - Three steps run strictly in input order (recorded) and yield three StatusPass
//     results; empty input returns nil.
func TestStepsSerial_RunsInOrder(t *testing.T) {
	t.Parallel()

	var order []string

	callback := func(name string) engine.Step {
		return engine.Step{
			Name: name, Group: "g", Run: func(_ context.Context) error {
				order = append(
					order,
					name,
				) // no lock: serial execution guarantees no race

				return nil
			},
			WarnOnly: false,
		}
	}

	results := engine.Steps{
		callback(textA),
		callback(textB),
		callback(textC),
	}.Serial(
		context.Background(),
	)
	require.Len(t, results, wantResultsLenOrder)

	for _, r := range results {
		assert.Equal(t, types.StatusPass, r.Status)
	}

	assert.Equal(t, []string{textA, textB, textC}, order, "steps must run in input order")

	assert.Nil(t, engine.Steps{}.Serial(context.Background()), "empty input returns nil")
}

const (
	// itemsCollected is the items collected the tests use.
	itemsCollected = 3

	// itemsUnitsCollected is the items units collected the tests use.
	itemsUnitsCollected = 4

	// makeInputFeeding is the make input feeding the tests use.
	makeInputFeeding = 8

	// wantGateStartPhasesLen is the expected gate start phases len.
	wantGateStartPhasesLen = 2

	// wantPassed is the expected passed.
	wantPassed = 4

	// wantResultsLenFail is the expected results len fail.
	wantResultsLenFail = 3

	// wantResultsLenOrder is the expected results len order.
	wantResultsLenOrder = 3

	// wantResultsLenResults is the expected results len results.
	wantResultsLenResults = 2
)

const (
	// textExpectedEachStepDelivered is the literal shown below.
	textExpectedEachStepDelivered = "expected each step delivered exactly once, got %v"

	// textJob1 is the literal "job-1".
	textJob1 = "job-1"

	// textPanicMe is the literal "panic-me".
	textPanicMe = "panic-me"

	// textPhaseA is the literal "phase-a".
	textPhaseA = "phase-a"
)
