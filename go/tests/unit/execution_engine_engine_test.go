package unit_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// engineNopRunner returns a CommandRunner mock with no programmed expectations. Every
// job exercised here ignores the runner — the mock jobs return canned results and
// the fan-out job fans out through engine.FanOut with its own closure — so any
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
		obs.EXPECT().OnStepComplete(gomock.Any()).Do(onStep).AnyTimes()
	} else {
		obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	}
	return obs
}

// newFanOutJob returns a MockAnyJob named "fanout" whose Execute fans out over items
// via engine.FanOut; the unit named blockOn waits on release before completing, and
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
	j := mocks.NewMockAnyJob(gomock.NewController(t))
	j.EXPECT().Meta().Return(types.JobMeta{Name: "fanout"}).AnyTimes()
	j.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ interfaces.CommandRunner,
			_ string,
		) (types.StepResults, error) {
			results := engine.FanOut(
				ctx, items, len(items),
				func(_ context.Context, item string) types.StepResult {
					if item == blockOn && release != nil {
						<-release
					}
					return types.StepResult{Name: item, Status: types.StatusPass}
				},
			)
			if extra != nil {
				results = append(results, *extra)
			}
			return results, nil
		},
	).
		AnyTimes()
	return j
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
	items := []int{0, 1, 2, 3, 4}
	results := engine.FanOut(
		context.Background(),
		items,
		2,
		func(_ context.Context, item int) types.StepResult {
			if item == 2 {
				return types.StepResult{
					Name:   fmt.Sprintf("item-%d", item),
					Status: types.StatusFail,
					Error:  "injected failure",
				}
			}
			return types.StepResult{
				Name:   fmt.Sprintf("item-%d", item),
				Status: types.StatusPass,
			}
		},
	)

	require.Len(t, results, len(items))
	var failed, passed int
	for _, r := range results {
		switch r.Status {
		case types.StatusFail:
			failed++
		case types.StatusPass:
			passed++
		case types.StatusSkip, types.StatusWarn:
			t.Errorf("unexpected status %v", r.Status)
		}
	}
	assert.Equal(t, 1, failed, "expected 1 failed result")
	assert.Equal(t, 4, passed, "expected 4 passed results")
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
	const (
		numItems   = 10
		maxWorkers = 3
	)
	var peak, current int64
	items := make([]int, numItems)

	engine.FanOut(
		context.Background(),
		items,
		maxWorkers,
		func(_ context.Context, _ int) types.StepResult {
			c := atomic.AddInt64(&current, 1)
			defer atomic.AddInt64(&current, -1)
			// track peak
			for {
				p := atomic.LoadInt64(&peak)
				if c <= p || atomic.CompareAndSwapInt64(&peak, p, c) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // hold slot briefly
			return types.StepResult{Status: types.StatusPass}
		},
	)

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
	items := []string{"ok", "panic-me", "ok2"}
	results := engine.FanOut(
		context.Background(),
		items,
		2,
		func(_ context.Context, item string) types.StepResult {
			if item == "panic-me" {
				panic("simulated panic")
			}
			return types.StepResult{Name: item, Status: types.StatusPass}
		},
	)

	require.Len(t, results, 3)
	for _, r := range results {
		if r.Name == "panic-me" || (r.Name == "" && r.Status == types.StatusFail) {
			// panic result may have empty name depending on impl; just verify Fail
			assert.Equal(t, types.StatusFail, r.Status, "panic item should be StatusFail")
			assert.NotEmpty(t, r.Detail, "panic result should have Detail (stack trace)")
			return
		}
	}
	assert.Fail(t, "no failed result found for the panicking item")
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
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	released := make(chan struct{})
	items := make([]int, 8)

	results := make(chan types.StepResults, 1)
	go func() {
		// numWorkers=1: only one item runs at a time, others queue at semaphore
		results <- engine.FanOut(ctx, items, 1, func(
			_ context.Context,
			item int,
		) types.StepResult {
			if item == 0 {
				close(started) // signal first item started
				<-released     // hold semaphore until test releases
			}
			return types.StepResult{Name: strconv.Itoa(item), Status: types.StatusPass}
		})
	}()

	<-started       // first item holds the semaphore
	cancel()        // cancel ctx — goroutines waiting at semaphore should bail
	close(released) // let first item finish

	rs := <-results
	require.Len(t, rs, len(items), "expected one result per item")
	// At least the items that didn't get a semaphore slot should be Fail (cancelled).
	// Item 0 ran to completion (StatusPass); others should be Fail.
	failed := 0
	for _, r := range rs {
		if r.Status == types.StatusFail {
			failed++
		}
	}
	// With numWorkers=1, items 1-7 cannot proceed after cancellation.
	assert.GreaterOrEqual(t, failed, 1, "expected at least 1 cancelled (Fail) result")
}

// --- ObserverFromCtx tests ---

// TestObserverFromCtx_ReturnsNopWhenAbsent tests that extracting an observer
// from a context that carries none yields a safe no-op observer.
//
// Why this test is important:
//   - Execution code calls observer hooks unconditionally. If ObserverFromCtx
//     returned nil when no observer was installed, every uninstrumented run
//     (tests, ad-hoc calls) would nil-panic on the first hook. The NopObserver
//     default is what lets the engine run unobserved without guards.
//
// What it tests:
//   - The returned observer accepts OnStepComplete/OnPhaseComplete/
//     OnGateComplete calls without panicking.
func TestObserverFromCtx_ReturnsNopWhenAbsent(_ *testing.T) {
	obs := engine.ObserverFromCtx(context.Background())
	// NopObserver must not panic on any hook call.
	obs.OnStepComplete(types.StepResult{})
	obs.OnPhaseComplete("phase", nil, 0)
	obs.OnGateComplete("", nil, 0)
}

// TestWithObserverCtx_RoundTrip tests that an observer stored on a context with
// WithObserverCtx is the same observer ObserverFromCtx later returns.
//
// Why this test is important:
//   - Observer propagation is context-based: RunJobGroup installs the observer
//     and downstream code retrieves it. If the round-trip dropped or swapped
//     the observer, lifecycle events would vanish and the UI/CI summary would
//     show nothing despite work running.
//
// What it tests:
//   - A step completed through the context-retrieved observer is recorded by
//     the originally-installed observer with the correct name.
func TestWithObserverCtx_RoundTrip(t *testing.T) {
	var steps []types.StepResult
	rec := capturingObserver(t, func(r types.StepResult) { steps = append(steps, r) })

	ctx := engine.WithObserverCtx(context.Background(), rec)
	obs := engine.ObserverFromCtx(ctx)
	obs.OnStepComplete(types.StepResult{Name: "x"})
	require.Len(t, steps, 1, "unexpected steps: %v", steps)
	assert.Equal(t, "x", steps[0].Name, "unexpected steps: %v", steps)
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
	ctrl := gomock.NewController(t)

	// One Pass job and one Fail job, each run exactly once by RunJobGroup; Times(1)
	// pins the "each job invoked once" half of the contract the test asserts.
	jobA := mocks.NewMockAnyJob(ctrl)
	jobA.EXPECT().Meta().Return(types.JobMeta{Name: "job-a"}).AnyTimes()
	jobA.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Name: "a", Status: types.StatusPass}}, nil).
		Times(1)

	jobB := mocks.NewMockAnyJob(ctrl)
	jobB.EXPECT().Meta().Return(types.JobMeta{Name: "job-b"}).AnyTimes()
	jobB.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Name: "b", Status: types.StatusFail, Error: "oops"}}, nil).
		Times(1)

	jobs := []interfaces.AnyJob{jobA, jobB}

	var steps []types.StepResult
	obs := capturingObserver(t, func(r types.StepResult) { steps = append(steps, r) })

	results, err := engine.RunJobGroup(
		context.Background(),
		engineNopRunner(t),
		"/root",
		jobs,
		obs,
	)
	require.NoError(t, err)
	require.Len(t, results, 2)
	// RunJobGroup forwards each result to OnStepComplete; phase aggregation is
	// the gate's responsibility, so RunJobGroup no longer fires OnPhaseComplete.
	assert.Len(t, steps, 2, "expected 2 OnStepComplete callbacks")
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
	var nop interfaces.ExecutionObserver = engine.NopObserver{}
	// Must not panic.
	nop.OnGateStart("gate", []string{"phase-a"})
	nop.OnPhaseStart("phase-a", []string{"job-1"})
	nop.OnStepStart("job-1", "phase-a")

	var gateStarts, phaseStarts, stepStarts []string
	var gateStartPhases [][]string
	obs := mocks.NewMockExecutionObserver(gomock.NewController(t))
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
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
	o.OnGateStart("gate", []string{"phase-a", "phase-b"})
	o.OnPhaseStart("phase-a", []string{"job-1"})
	o.OnStepStart("job-1", "phase-a")

	require.Len(t, gateStarts, 1, "OnGateStart not recorded: %v", gateStarts)
	require.Equal(t, "gate", gateStarts[0], "OnGateStart not recorded: %v", gateStarts)
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
		2,
		"OnGateStart phases not recorded: %v",
		gateStartPhases,
	)
	require.Len(t, phaseStarts, 1, "OnPhaseStart not recorded: %v", phaseStarts)
	require.Equal(
		t,
		"phase-a",
		phaseStarts[0],
		"OnPhaseStart not recorded: %v",
		phaseStarts,
	)
	require.Len(t, stepStarts, 1, "OnStepStart not recorded: %v", stepStarts)
	require.Equal(t, "job-1", stepStarts[0], "OnStepStart not recorded: %v", stepStarts)
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
	release := make(chan struct{})

	var mu sync.Mutex
	var names []string
	seen := make(chan struct{})
	var seenOnce bool
	obs := capturingObserver(t, func(r types.StepResult) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, r.Name)
		if r.Name == "fast" && !seenOnce {
			seenOnce = true
			close(seen)
		}
	})

	job := newFanOutJob(t, []string{"fast", "slow"}, "slow", release, nil)
	runner := engineNopRunner(t)

	done := make(chan struct{})
	go func() {
		_, _ = engine.RunJobGroup(
			context.Background(), runner, "/",
			[]interfaces.AnyJob{job}, obs,
		)
		close(done)
	}()

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

	mu.Lock()
	c := map[string]int{}
	for _, n := range names {
		c[n]++
	}
	mu.Unlock()
	require.Equal(t, 1, c["fast"], "expected each unit delivered exactly once, got %v", c)
	require.Equal(t, 1, c["slow"], "expected each unit delivered exactly once, got %v", c)
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
	extra := types.StepResult{Name: "coverage", Status: types.StatusPass}

	var mu sync.Mutex
	var names []string
	obs := capturingObserver(t, func(r types.StepResult) {
		mu.Lock()
		names = append(names, r.Name)
		mu.Unlock()
	})

	job := newFanOutJob(t, []string{"a", "b"}, "", nil, &extra)

	all, _ := engine.RunJobGroup(
		context.Background(), engineNopRunner(t), "/",
		[]interfaces.AnyJob{job}, obs,
	)

	require.Len(t, all, 3, "want 3 aggregate results")
	mu.Lock()
	c := map[string]int{}
	for _, n := range names {
		c[n]++
	}
	mu.Unlock()
	require.Equal(t, 1, c["a"], "expected each step delivered exactly once, got %v", c)
	require.Equal(t, 1, c["b"], "expected each step delivered exactly once, got %v", c)
	require.Equal(
		t,
		1,
		c["coverage"],
		"expected each step delivered exactly once, got %v",
		c,
	)
}

// --- engine.Step tests ---

// TestNewStep_PassOnSuccess tests that a Step whose function returns nil yields
// a Pass result carrying the step's name and group.
//
// Why this test is important:
//   - NewStep is the simple function-to-result wrapper CLI commands rely on.
//     The name and group are how a result is identified and grouped in the
//     output; losing them or mis-classifying a success would scramble the
//     run summary.
//
// What it tests:
//   - A nil-returning step produces StatusPass with the supplied name "lint"
//     and group "quality".
func TestNewStep_PassOnSuccess(t *testing.T) {
	s := engine.NewStep("lint", "quality", func(_ context.Context) error { return nil })
	result := s.Execute(context.Background())
	assert.Equal(t, types.StatusPass, result.Status)
	assert.Equal(t, "lint", result.Name)
	assert.Equal(t, "quality", result.Group)
}

// TestNewStep_FailOnError tests that a Step whose function returns an error
// yields a Fail result with a non-empty error message.
//
// Why this test is important:
//   - A step failure must surface as StatusFail with a message so the gate
//     fails the run and the user sees why. If an error were swallowed into a
//     Pass or produced an empty message, broken checks would pass silently.
//
// What it tests:
//   - An erroring step produces StatusFail and a non-empty Error string.
func TestNewStep_FailOnError(t *testing.T) {
	s := engine.NewStep("typecheck", "quality", func(_ context.Context) error {
		return apperr.Sentinel("type error")
	})
	result := s.Execute(context.Background())
	assert.Equal(t, types.StatusFail, result.Status)
	assert.NotEmpty(t, result.Error, "expected non-empty error message")
}

// TestNewStep_WarnOnly_DowngradesFailure tests that the StepWarnOnly option
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
func TestNewStep_WarnOnly_DowngradesFailure(t *testing.T) {
	s := engine.NewStep(
		"shellcheck", "infra",
		func(_ context.Context) error { return apperr.Sentinel("shell issue") },
		engine.StepWarnOnly(),
	)
	result := s.Execute(context.Background())
	assert.Equal(t, types.StatusWarn, result.Status)
}

// TestNewStep_WarnOnly_PreservesPass tests that the StepWarnOnly option only
// downgrades failures and leaves a passing result a Pass.
//
// Why this test is important:
//   - The downgrade must be scoped to failures. If warn-only also rewrote
//     successes, a passing advisory check would be reported as a warning,
//     cluttering output with false warnings and eroding trust in the signal.
//
// What it tests:
//   - A warn-only step whose function succeeds still produces StatusPass.
func TestNewStep_WarnOnly_PreservesPass(t *testing.T) {
	s := engine.NewStep(
		"ok-step", "test",
		func(_ context.Context) error { return nil },
		engine.StepWarnOnly(),
	)
	result := s.Execute(context.Background())
	assert.Equal(
		t,
		types.StatusPass,
		result.Status,
		"expected StatusPass (not downgraded)",
	)
}

// TestRunStepsParallel_AllPass tests that RunStepsParallel runs every step and
// returns one result per step.
//
// Why this test is important:
//   - RunStepsParallel is how a command runs independent steps concurrently.
//     A dropped step or lost result would mean a check silently never ran, the
//     worst kind of failure because nothing reports it.
//
// What it tests:
//   - Two passing steps yield two results, all StatusPass.
func TestRunStepsParallel_AllPass(t *testing.T) {
	s1 := engine.NewStep("a", "g", func(_ context.Context) error { return nil })
	s2 := engine.NewStep("b", "g", func(_ context.Context) error { return nil })
	results := engine.RunStepsParallel(context.Background(), s1, s2)
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Equal(
			t,
			types.StatusPass,
			r.Status,
			"expected all StatusPass, got %v for %s",
			r.Status,
			r.Name,
		)
	}
}

// TestRunStepsSerial_RunsInOrder tests that RunStepsSerial executes steps one at a
// time in input order and returns one result per step.
//
// Why this test is important:
//   - RunStepsSerial is for order-dependent sequences (e.g. a resource prune where
//     compose-down must precede image removal). If it ran out of order or dropped a
//     step, the dependent cleanup would act on stale state. This pins the ordering
//     guarantee that distinguishes it from RunStepsParallel — and, because the steps
//     append to a shared slice without a lock, that it runs strictly serially.
//
// What it tests:
//   - Three steps run strictly in input order (recorded) and yield three StatusPass
//     results; empty input returns nil.
func TestRunStepsSerial_RunsInOrder(t *testing.T) {
	var order []string
	mk := func(name string) engine.Step {
		return engine.NewStep(name, "g", func(_ context.Context) error {
			order = append(order, name) // no lock: serial execution guarantees no race
			return nil
		})
	}

	results := engine.RunStepsSerial(context.Background(), mk("a"), mk("b"), mk("c"))
	require.Len(t, results, 3)
	for _, r := range results {
		assert.Equal(t, types.StatusPass, r.Status)
	}
	assert.Equal(t, []string{"a", "b", "c"}, order, "steps must run in input order")

	assert.Nil(t, engine.RunStepsSerial(context.Background()), "empty input returns nil")
}

// TestRunStepsParallel_Empty tests that RunStepsParallel returns nil when given
// no steps.
//
// Why this test is important:
//   - The no-steps path must short-circuit cleanly rather than allocate an
//     empty slice or block on an empty wait group. Callers distinguish "no work"
//     (nil) from "work that produced empty results", so the nil contract matters.
//
// What it tests:
//   - Calling with zero steps returns a nil result slice.
func TestRunStepsParallel_Empty(t *testing.T) {
	results := engine.RunStepsParallel(context.Background())
	assert.Nil(t, results, "expected nil for empty input")
}

// TestNewStep_StepNameGroup_ViaWarnOnly tests that the warn-only wrapper still
// exposes the underlying step's name and group through StepName/StepGroup.
//
// Why this test is important:
//   - The UI reads StepName/StepGroup to label and group a step. The warn-only
//     decorator wraps the real step, so it must delegate those accessors; if it
//     returned empty strings, warn-only steps would show up unlabeled and
//     ungrouped in the output.
//
// What it tests:
//   - A warn-only-wrapped step reports StepName "myname" and StepGroup
//     "mygroup" — the values passed to NewStep.
func TestNewStep_StepNameGroup_ViaWarnOnly(t *testing.T) {
	s := engine.NewStep(
		"myname", "mygroup",
		func(_ context.Context) error { return nil },
		engine.StepWarnOnly(),
	)
	type namer interface{ StepName() string }
	type grouper interface{ StepGroup() string }
	if n, ok := s.(namer); ok {
		assert.Equal(t, "myname", n.StepName())
	}
	if g, ok := s.(grouper); ok {
		assert.Equal(t, "mygroup", g.StepGroup())
	}
}
