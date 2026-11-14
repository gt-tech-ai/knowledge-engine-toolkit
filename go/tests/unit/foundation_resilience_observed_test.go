package unit_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	gb "github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/adaptive"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// The subject the observed-layer tests record under.
const (
	// observedTier is the tier label.
	observedTier = "client"

	// observedName is the subject's name label.
	observedName = "x"

	// observedOp is the operation label.
	observedOp = "Get"

	// observedOperation is the operation label's name.
	observedOperation = "operation"

	// observedReason is the reason label's name.
	observedReason = "reason"

	// observedRejections is the rejections series.
	observedRejections = "resilience_rejections_total"

	// observedNever is the retry case's "never refuse" budget position.
	observedNever = -1

	// observedDown is a transient dependency failure's message.
	observedDown = "down"

	// observedTwice is two of something: two retries, or a refusal after two attempts.
	observedTwice = 2
)

// layerHarness is one layer test's registry, the Observer recording into it and the
// mock logger receiving its Warn lines.
type layerHarness struct {
	// registry is the in-process Prometheus registry the recorder writes to.
	registry *prometheus.Registry

	// logger is the strict mock logger; a Warn the test did not expect fails it.
	logger *mocks.MockLogger

	// tier is the tier label the recorder binds and value reads.
	tier string

	// obs is the Observer the component under test reports through.
	obs decorate.Observer
}

// newObserved builds an Observer for the client-tier subject observedName over a
// fresh registry and a strict mock logger.
func newObserved(t *testing.T) layerHarness {
	t.Helper()

	return newHarness(t, observedTier)
}

// newStandalone builds an Observer for a primitive named observedName used outside a
// tier: its recorder and its Warn lines name tier "standalone".
func newStandalone(t *testing.T) layerHarness {
	t.Helper()

	return newHarness(t, series.TierStandalone)
}

// newHarness builds an Observer for the subject observedName in tier over a fresh
// registry and a strict mock logger.
func newHarness(t *testing.T, tier string) layerHarness {
	t.Helper()

	registry := prometheus.NewRegistry()
	logger := mocks.NewMockLogger(gomock.NewController(t))

	return layerHarness{
		registry: registry,
		logger:   logger,
		tier:     tier,
		obs: series.NewObserver(
			prom.NewFromRegistry(registry), logger, tier, observedName,
		),
	}
}

// value sums the samples of metric labelled with the harness's tier, the subject's name
// and label=labelValue; a series never recorded reads 0.
func (o *layerHarness) value(t *testing.T, metric, label, labelValue string) float64 {
	t.Helper()

	families, err := o.registry.Gather()
	require.NoError(t, err)

	want := map[string]string{
		"tier": o.tier, textName: observedName, label: labelValue,
	}

	var total float64

	for _, family := range families {
		if family.GetName() == metric {
			total += familySum(family, want)
		}
	}

	return total
}

// familySum sums the counter and gauge values of family's samples that carry every
// label pair in want.
func familySum(family *dto.MetricFamily, want map[string]string) float64 {
	var total float64

	for _, sample := range family.GetMetric() {
		if labelled(sample, want) {
			total += sample.GetCounter().GetValue() + sample.GetGauge().GetValue()
		}
	}

	return total
}

// labelled reports whether sample carries every label pair in want.
func labelled(sample *dto.Metric, want map[string]string) bool {
	matched := 0

	for _, pair := range sample.GetLabel() {
		if value, ok := want[pair.GetName()]; ok && value == pair.GetValue() {
			matched++
		}
	}

	return matched == len(want)
}

// retryCase is one row of the retry layer's table.
type retryCase struct {
	// results are what the operation returns on each attempt, in order; the mock
	// retrier stops at the first nil, at a permanent error, or when they run out.
	results []error

	// refuseAt is the attempt count after which the budget refuses the next retry, or
	// observedNever.
	refuseAt int

	// want is what the layer must record.
	want retryWant

	// inTx runs the operation inside a transaction, where the layer never retries.
	inTx bool
}

// retryWant is what one retry case must record, positionally: retried, exhausted,
// refused, warns.
type retryWant struct {
	// retried is the expected resilience_retries_total.
	retried float64

	// exhausted is the expected resilience_retry_exhausted_total.
	exhausted float64

	// refused is the expected budget_exhausted rejections.
	refused float64

	// warns is the expected count of "retries exhausted" Warn lines.
	warns int
}

// scriptedRetrier returns a mock Retrier that drives the operation through runScript.
func scriptedRetrier(t *testing.T, row *retryCase) *mocks.MockRetrier {
	t.Helper()

	retrier := mocks.NewMockRetrier(gomock.NewController(t))
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, operation func() error) error {
			return runScript(row, operation)
		},
	).MaxTimes(1)

	return retrier
}

// runScript runs operation once per result until one succeeds or is permanent, or until
// the budget refuses the retry after refuseAt attempts, the way the exponential
// retrier does.
func runScript(row *retryCase, operation func() error) error {
	var err error

	for attempt := range row.results {
		if attempt > 0 && attempt == row.refuseAt {
			return apperr.Wrap(
				budget.ErrRetryBudgetExhausted, apperr.CodeUnavailable, "retry",
			)
		}

		err = operation()
		if !apperr.IsTransient(err) {
			return err
		}
	}

	return err
}

// retryCases is the retry layer's table: each row's per-attempt results, the budget's
// refusal point and what the layer must record.
func retryCases() map[string]retryCase {
	transient := apperr.New(apperr.CodeUnavailable, observedDown)
	permanent := apperr.New(apperr.CodeInvalidInput, "bad")
	canceled := apperr.Wrap(context.Canceled, apperr.CodeCanceled, "caller left")

	return map[string]retryCase{
		"success": {
			results: []error{nil}, refuseAt: observedNever, inTx: false,
			want: retryWant{0, 0, 0, 0},
		},
		"recovers": {
			results:  []error{transient, transient, nil},
			refuseAt: observedNever, inTx: false,
			want: retryWant{observedTwice, 0, 0, 0},
		},
		"exhausted": {
			results:  []error{transient, transient, transient},
			refuseAt: observedNever, inTx: false,
			want: retryWant{observedTwice, 1, 0, 1},
		},
		"permanent": {
			results: []error{permanent}, refuseAt: observedNever, inTx: false,
			want: retryWant{0, 0, 0, 0},
		},
		"refused first retry": {
			results: []error{transient, transient}, refuseAt: 1, inTx: false,
			want: retryWant{0, 0, 1, 0},
		},
		"refused after one retry": {
			results:  []error{transient, transient, transient},
			refuseAt: observedTwice, inTx: false,
			want: retryWant{1, 0, 1, 0},
		},
		"in transaction": {
			results: []error{transient}, refuseAt: observedNever, inTx: true,
			want: retryWant{0, 0, 0, 0},
		},
		"caller cancelled after a retry": {
			results: []error{transient, canceled}, refuseAt: observedNever, inTx: false,
			want: retryWant{1, 0, 0, 0},
		},
	}
}

// TestRetryMW_RecordsRetriesAndExhaustion tests that the retry layer counts each retry,
// each operation that failed after retrying, and each retry the budget refused.
//
// Why this test is important:
//   - retries were invisible: an operator could not tell whether a dependency's
//     failures were being retried, exhausted, or refused by the budget. Counting one
//     operation as both exhausted and refused would double every alert on it.
//
// What it tests:
//   - one success records nothing; two failures then success record 2 retries and no
//     exhaustion; always failing records retries, 1 exhausted and 1 Warn; a permanent
//     first error records nothing; a budget refusal records 1 budget_exhausted
//     rejection and no exhaustion; an in-transaction run records nothing; a call its
//     caller cancelled after a retry records the retry and no exhaustion or Warn.
func TestRetryMW_RecordsRetriesAndExhaustion(t *testing.T) {
	t.Parallel()

	for name, row := range retryCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newObserved(t)
			harness.logger.EXPECT().Warn("retries exhausted",
				"tier", observedTier, textName, observedName, observedOperation, observedOp,
				"attempts", len(row.results)).
				Times(row.want.warns)

			runRetried(t, &harness, &row)

			require.InDelta(
				t,
				row.want.retried,
				harness.value(
					t,
					"resilience_retries_total",
					observedOperation,
					observedOp,
				),
				0,
			)
			require.InDelta(t, row.want.exhausted, harness.value(t,
				"resilience_retry_exhausted_total", observedOperation, observedOp), 0)
			require.InDelta(t, row.want.refused, harness.value(t,
				observedRejections, observedReason, "budget_exhausted"), 0)
		})
	}
}

// runRetried runs one operation through the retry layer, returning each attempt's
// result from row in turn.
func runRetried(t *testing.T, harness *layerHarness, row *retryCase) {
	t.Helper()

	ctx := context.Background()
	if row.inTx {
		ctx = interfaces.WithInTx(ctx)
	}

	attempt := 0
	retry := decorate.NewRetry(scriptedRetrier(t, row), harness.obs)

	_, _ = decorate.Exec(ctx, retry, observedOp,
		func(context.Context) (struct{}, error) {
			err := row.results[attempt]
			attempt++

			return struct{}{}, err
		})
}

// TestRetryMW_ZeroObserverRecordsNothing tests that a retry layer built with the zero
// Observer runs without reporting.
//
// Why this test is important:
//   - a layer built without metrics or logging (the switches off) must neither panic
//     on its nil recorder and logger nor change the operation's result.
//
// What it tests:
//   - an always-failing operation through NewRetry with a zero Observer returns the
//     operation's error without a panic.
func TestRetryMW_ZeroObserverRecordsNothing(t *testing.T) {
	t.Parallel()

	transient := apperr.New(apperr.CodeUnavailable, observedDown)
	row := retryCase{
		results:  []error{transient, transient},
		refuseAt: observedNever,
		inTx:     false,
		want:     retryWant{0, 0, 0, 0},
	}

	retry := decorate.NewRetry(scriptedRetrier(t, &row), unobserved())

	require.NotPanics(t, func() {
		_, err := decorate.Exec(context.Background(), retry, observedOp,
			func(context.Context) (struct{}, error) { return struct{}{}, transient })
		require.ErrorIs(t, err, transient)
	})
}

// The timeout layer cases' durations.
const (
	// observedDeadline is the layer's timeout in the timeout cases.
	observedDeadline = 20 * time.Millisecond

	// observedLongDeadline is a deadline no case reaches.
	observedLongDeadline = time.Second

	// observedOverrun is how long an operation that ignores its context runs: past
	// observedDeadline.
	observedOverrun = 2 * observedDeadline
)

// timeoutCase is one row of the timeout layer's table.
type timeoutCase struct {
	// operation is the decorated operation.
	operation func(context.Context) error

	// layer is the layer's timeout; zero is a passthrough.
	layer time.Duration

	// caller is the caller's own deadline, or zero for none.
	caller time.Duration

	// want is the expected resilience_timeouts_total.
	want float64
}

// TestTimeoutMW_RecordsOwnDeadlineOnly tests that the timeout layer counts an operation
// its own deadline cut, and nothing else.
//
// Why this test is important:
//   - a timeout counted for the caller's deadline, or for an unrelated error that
//     happened to return late, would blame the dependency for time it did not spend.
//
// What it tests:
//   - an operation slower than the layer's deadline counts 1; a fast one, one cut by
//     the caller's shorter deadline, one returning a non-timeout error after the
//     deadline, and a zero-timeout passthrough count 0.
func TestTimeoutMW_RecordsOwnDeadlineOnly(t *testing.T) {
	t.Parallel()

	waits := func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}
	fast := func(context.Context) error { return nil }
	late := func(context.Context) error {
		time.Sleep(observedOverrun)

		return apperr.New(apperr.CodeInvalidInput, "bad")
	}

	cases := map[string]timeoutCase{
		"cut by the layer": {
			operation: waits, layer: observedDeadline, caller: 0, want: 1,
		},
		"fast": {operation: fast, layer: observedDeadline, caller: 0, want: 0},
		"caller": {
			operation: waits, layer: observedLongDeadline, caller: observedDeadline,
			want: 0,
		},
		"late error": {operation: late, layer: observedDeadline, caller: 0, want: 0},
		"zero":       {operation: waits, layer: 0, caller: observedDeadline, want: 0},
	}

	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newObserved(t)

			ctx := context.Background()

			if row.caller > 0 {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, row.caller)
				defer cancel()
			}

			_, _ = decorate.Exec(ctx, decorate.NewTimeout(row.layer, harness.obs),
				observedOp, func(ctx context.Context) (struct{}, error) {
					return struct{}{}, row.operation(ctx)
				})

			require.InDelta(t, row.want, harness.value(t,
				"resilience_timeouts_total", observedOperation, observedOp), 0)
		})
	}
}

// TestCircuitBreakerMW_RecordsRejections tests that the circuit-breaker layer counts a
// call the breaker refused, and not a failure the dependency itself returned.
//
// Why this test is important:
//   - an open breaker sheds every call to a dependency; without the count an operator
//     sees only CodeUnavailable and cannot tell a shed call from a dependency outage,
//     which share that code.
//
// What it tests:
//   - an Execute refused with gobreaker's open-state error counts one breaker_open
//     rejection; the dependency's own CodeUnavailable and a success count none.
func TestCircuitBreakerMW_RecordsRejections(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// refusal is what the mock breaker returns without running the operation, or
		// nil to run it.
		refusal error

		// result is the operation's own result when the breaker runs it.
		result error

		// want is the expected breaker_open rejections.
		want float64
	}{
		"open": {
			refusal: apperr.Wrap(gb.ErrOpenState, apperr.CodeUnavailable, "open"),
			result:  nil,
			want:    1,
		},
		"dependency down": {
			refusal: nil,
			result:  apperr.New(apperr.CodeUnavailable, observedDown),
			want:    0,
		},
		"success": {refusal: nil, result: nil, want: 0},
	}

	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newObserved(t)
			breaker := refusingBreaker(t, row.refusal)

			_, _ = decorate.Exec(context.Background(),
				decorate.NewCircuitBreaker(breaker, harness.obs), observedOp,
				func(context.Context) (struct{}, error) { return struct{}{}, row.result })

			require.InDelta(t, row.want, harness.value(t,
				observedRejections, observedReason, "breaker_open"), 0)
		})
	}
}

// refusingBreaker returns a mock CircuitBreaker that returns refusal without running
// the operation, or runs it when refusal is nil.
func refusingBreaker(t *testing.T, refusal error) *mocks.MockCircuitBreaker {
	t.Helper()

	breaker := mocks.NewMockCircuitBreaker(gomock.NewController(t))
	breaker.EXPECT().
		Execute(gomock.Any()).
		DoAndReturn(func(operation func() error) error {
			if refusal != nil {
				return refusal
			}

			return operation()
		})

	return breaker
}

// observedBreaker returns a real gobreaker-backed breaker that opens on one transient
// failure and half-opens after observedDeadline, reporting through obs.
func observedBreaker(t *testing.T, obs *series.Observer) interfaces.CircuitBreaker {
	t.Helper()

	cfg := circuitbreaker.DefaultConfig(observedName)

	cfg.Observer = obs
	cfg.ConsecutiveFailures = 1
	cfg.Timeout = observedDeadline

	breaker, err := circuitbreaker.NewFromConfig(cfg)
	require.NoError(t, err)

	return breaker
}

// TestBreaker_ReportsStateAndTransitions tests that a gobreaker breaker reports its
// state, each move, and one Warn when it opens.
//
// Why this test is important:
//   - a breaker held open sheds every call to its dependency; the state gauge and the
//     Warn are how an operator learns it opened and whether it recovered.
//
// What it tests:
//   - a new breaker reads closed; one transient failure opens it (state 2, one
//     transition to open, one Warn naming tier and name); after its timeout a success
//     moves it to half_open then closed (state 0).
func TestBreaker_ReportsStateAndTransitions(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	harness.logger.EXPECT().
		Warn("circuit breaker opened", "tier", observedTier, textName, observedName)

	breaker := observedBreaker(t, &harness.obs)
	state := func() float64 {
		return harness.value(t, "resilience_breaker_state", textName, observedName)
	}
	moved := func(to string) float64 {
		return harness.value(t, "resilience_breaker_transitions_total", "to", to)
	}

	require.InDelta(t, 0, state(), 0)

	_ = breaker.Execute(func() error {
		return apperr.New(apperr.CodeUnavailable, observedDown)
	})

	require.InDelta(t, observedTwice, state(), 0, "open is gauge number 2")
	require.InDelta(t, 1, moved(string(series.BreakerStateOpen)), 0)

	time.Sleep(observedOverrun)
	require.NoError(t, breaker.Execute(func() error { return nil }))

	require.InDelta(t, 1, moved(string(series.BreakerStateHalfOpen)), 0)
	require.InDelta(t, 1, moved(string(series.BreakerStateClosed)), 0)
	require.InDelta(t, 0, state(), 0)
}

// TestBreaker_WithoutObserverReportsNothing tests that a breaker built without an
// Observer still opens and closes without a panic.
//
// Why this test is important:
//   - most standalone breakers are built without metrics; the state callback must be
//     safe on a nil Observer.
//
// What it tests:
//   - a nil-Observer breaker trips on one transient failure and refuses the next call.
func TestBreaker_WithoutObserverReportsNothing(t *testing.T) {
	t.Parallel()

	breaker := observedBreaker(t, nil)

	require.NotPanics(t, func() {
		_ = breaker.Execute(func() error {
			return apperr.New(apperr.CodeUnavailable, observedDown)
		})
	})
	require.True(t, circuitbreaker.IsOpen(breaker.Execute(func() error { return nil })))
}

// TestBreakerConfig_NeverDecodesObserver tests that the breaker's Observer cannot be
// set from configuration.
//
// Why this test is important:
//   - the Observer is wired in code at the composition root; a config file that could
//     fill it would at best fail to decode and at worst build a half-set Observer.
//
// What it tests:
//   - decoding YAML and a mapstructure map that both carry an "observer" key into
//     circuitbreaker.Config leaves Observer nil and still decodes the name.
func TestBreakerConfig_NeverDecodesObserver(t *testing.T) {
	t.Parallel()

	var fromYAML circuitbreaker.Config

	require.NoError(t, yaml.Unmarshal(
		[]byte("name: payments\nobserver: {name: other, tier: client}\n"), &fromYAML,
	))
	require.Nil(t, fromYAML.Observer)
	require.Equal(t, "payments", fromYAML.Name)

	var fromMap circuitbreaker.Config

	require.NoError(t, mapstructure.Decode(map[string]any{
		"name":     "payments",
		"observer": map[string]any{"name": "other"},
	}, &fromMap))
	require.Nil(t, fromMap.Observer)
	require.Equal(t, "payments", fromMap.Name)
}

// TestBulkheadMW_RecordsShedCalls tests that the shedding bulkhead layer counts a call
// it shed, and that the waiting layer, which never sheds, counts none.
//
// Why this test is important:
//   - a server sheds load at its bulkhead with CodeResourceExhausted, the code a rate
//     limit uses too; the reason label is how an operator tells the two apart.
//
// What it tests:
//   - NewSheddingBulkhead over a full bulkhead counts one bulkhead_full rejection and
//     does not run the operation; NewBulkhead counts none.
func TestBulkheadMW_RecordsShedCalls(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	full := mocks.NewMockBulkhead(gomock.NewController(t))
	full.EXPECT().TryExecute(gomock.Any()).Return(adaptive.ErrBulkheadFull)
	full.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, operation func() error) error { return operation() },
	)

	ran := false
	operation := func(context.Context) (struct{}, error) {
		ran = true

		return struct{}{}, nil
	}

	_, err := decorate.Exec(context.Background(),
		decorate.NewSheddingBulkhead(full, harness.obs), observedOp, operation)
	require.True(t, apperr.Is(err, apperr.CodeResourceExhausted))
	require.False(t, ran, "a shed call never runs")

	_, err = decorate.Exec(context.Background(),
		decorate.NewBulkhead(full, harness.obs), observedOp, operation)
	require.NoError(t, err)

	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "bulkhead_full"), 0)
}

// TestAdaptiveBulkhead_ReportsLimit tests that the adaptive bulkhead reports its limit
// when built and after each adjustment.
//
// Why this test is important:
//   - the adaptive limit is the bulkhead's verdict on its dependency's health: a limit
//     pinned at its floor means the dependency is slow or failing, which nothing else
//     shows.
//
// What it tests:
//   - a new bulkhead reports its initial limit; a failed call shrinks it by the backoff
//     ratio; a fast successful call grows it by one.
func TestAdaptiveBulkhead_ReportsLimit(t *testing.T) {
	t.Parallel()

	const initial = 5

	harness := newObserved(t)
	cfg := adaptive.DefaultConfig()

	cfg.Recorder = harness.obs.Recorder
	cfg.InitialConcurrent = initial
	cfg.RTTThreshold = 0

	bulkhead := adaptive.New(cfg)
	limit := func() float64 {
		return harness.value(t, "resilience_bulkhead_limit", textName, observedName)
	}

	require.InDelta(t, initial, limit(), 0)

	_ = bulkhead.TryExecute(func() error {
		return apperr.New(apperr.CodeUnavailable, observedDown)
	})

	require.InDelta(t, initial-1, limit(), 0, "5 × 0.9 = 4.5 admits 4")

	require.NoError(t, bulkhead.TryExecute(func() error { return nil }))
	require.InDelta(t, initial, limit(), 0, "4.5 + 1 = 5.5 admits 5")
}

// TestRateLimitMW_RecordsRejections tests that the rate-limit layer counts an
// operation it refused, and nothing when the limiter allows it.
//
// Why this test is important:
//   - a rate-limited call fails with CodeResourceExhausted, the same code a shed call
//     carries; the reason label is the only way to tell a limit from overload.
//
// What it tests:
//   - Allow() false counts one rate_limited rejection and does not run the operation;
//     Allow() true counts none.
func TestRateLimitMW_RecordsRejections(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	limiter := mocks.NewMockRateLimiter(gomock.NewController(t))
	gomock.InOrder(
		limiter.EXPECT().Allow().Return(false),
		limiter.EXPECT().Allow().Return(true),
	)

	runs := 0
	operation := func(context.Context) (struct{}, error) {
		runs++

		return struct{}{}, nil
	}
	limited := decorate.NewRateLimit(limiter, harness.obs)

	_, err := decorate.Exec(context.Background(), limited, observedOp, operation)
	require.True(t, apperr.Is(err, apperr.CodeResourceExhausted))

	_, err = decorate.Exec(context.Background(), limited, observedOp, operation)
	require.NoError(t, err)

	require.Equal(t, 1, runs, "only the allowed call runs")
	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "rate_limited"), 0)
}

// TestBuild_WiresObserverByLayerSwitches tests that Build hands the breaker and the
// adaptive bulkhead an Observer bound to the tier and name, gated by the metrics and
// logging switches.
//
// Why this test is important:
//   - the switches are how a deployment turns observability off for one component; a
//     breaker that reported regardless would export series the config said not to,
//     and one that never got its Observer would leave its state invisible.
//
// What it tests:
//   - metrics and logging on: a tripped breaker reads open under tier and name, warns
//     once, and the adaptive bulkhead reports its initial limit.
//   - both off: the same breaker and bulkhead record nothing and log nothing.
func TestBuild_WiresObserverByLayerSwitches(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// switched turns the metrics and logging layers on.
		switched bool

		// want is the expected breaker state gauge and bulkhead limit.
		want float64

		// warns is the expected count of "circuit breaker opened" Warn lines.
		warns int
	}{
		"on":  {switched: true, want: observedTwice, warns: 1},
		"off": {switched: false, want: 0, warns: 0},
	}

	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newObserved(t)
			harness.logger.EXPECT().Warn("circuit breaker opened",
				"tier", observedTier, textName, observedName).Times(row.warns)

			deps := buildObserved(t, &harness, row.switched)

			_ = deps.Breaker.Execute(func() error {
				return apperr.New(apperr.CodeUnavailable, observedDown)
			})

			require.InDelta(t, row.want, harness.value(t,
				"resilience_breaker_state", textName, observedName), 0, "open is 2")
			require.InDelta(t, row.want, harness.value(t,
				"resilience_bulkhead_limit", textName, observedName), 0, "initial limit")
		})
	}
}

// buildObserved runs Build for the client-tier subject observedName with a one-failure
// breaker and an adaptive bulkhead, the metrics and logging layers switched to on, over
// the harness's registry and logger. The bulkhead starts at observedTwice.
func buildObserved(t *testing.T, harness *layerHarness, switched bool) decorate.Deps {
	t.Helper()

	layers := new(decoration.DefaultConfig().Clients).For(observedName)
	enabled := true

	layers.Metrics, layers.Logging = &switched, &switched
	layers.CircuitBreaker.Enabled = &enabled
	layers.CircuitBreaker.BreakerConfig.ConsecutiveFailures = 1
	layers.Bulkhead.Enabled = &enabled
	layers.Bulkhead.BulkheadConfig.Kind = "adaptive"
	layers.Bulkhead.BulkheadConfig.InitialConcurrent = observedTwice

	deps, err := decorate.Build(observedTier, observedName, &layers,
		fixtures.Build(func(row *decorate.Collaborators) {
			row.Metrics = prom.NewFromRegistry(harness.registry)
			row.Logger = harness.logger
		}),
	)
	require.NoError(t, err)

	return deps
}

// resilienceTolerantMetrics returns a strict mock Metrics that also accepts every
// resilience_* registration and write. A decorate build with the metrics layer on now
// registers those series for its breaker and bulkhead; a test that pins only the RED
// series stays strict about everything else.
func resilienceTolerantMetrics(ctrl *gomock.Controller) *mocks.MockMetrics {
	resilienceSeries := gomock.Cond(func(name any) bool {
		text, ok := name.(string)

		return ok && strings.HasPrefix(text, "resilience_")
	})

	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any()).AnyTimes()

	gauge := mocks.NewMockGauge(ctrl)
	gauge.EXPECT().Set(gomock.Any(), gomock.Any()).AnyTimes()

	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().Counter(resilienceSeries, gomock.Any(), gomock.Any()).
		Return(counter).AnyTimes()
	metrics.EXPECT().Gauge(resilienceSeries, gomock.Any(), gomock.Any()).
		Return(gauge).AnyTimes()

	return metrics
}

// TestObserved_NoResilienceLayerRegistersNothing tests that a component with the
// metrics layer on but no resilience layer exports no resilience_* series.
//
// Why this test is important:
//   - every decorated component builds its chains at boot; registering eleven
//     resilience families for one that has no retry, breaker, bulkhead, rate limit or
//     timeout inflates the scrape and breaks a consumer's strict metrics mock.
//
// What it tests:
//   - with the timeout zero and the other resilience layers off, building the
//     client-tier subject and running one operation never asks the provider for a
//     resilience_* series (a strict mock fails on one).
func TestObserved_NoResilienceLayerRegistersNothing(t *testing.T) {
	t.Parallel()

	cfg := decoration.DefaultConfig()

	cfg.Clients.Overrides[observedName] = fixtures.Build(func(row *decoration.Layers) {
		on, off := true, false

		row.Metrics = &on
		row.Timeout = new(time.Duration)
		row.Retry.Enabled = &off
		row.CircuitBreaker.Enabled = &off
		row.RateLimit.Enabled = &off
		row.Bulkhead.Enabled = &off
	})

	collaborators := fixtures.Build(func(row *decorate.Collaborators) {
		row.Metrics = redOnlyMetrics(gomock.NewController(t))
	})
	subject := decorate.NewDecorations(&cfg, collaborators).
		Subject(client.Tier(), observedName)

	_, err := subject.Exec(context.Background(), observedOp,
		func(context.Context) (any, error) { return struct{}{}, nil })
	require.NoError(t, err)
}

// redOnlyMetrics returns a strict mock Metrics that accepts any series except a
// resilience_* one: asking for one of those fails the test.
func redOnlyMetrics(ctrl *gomock.Controller) *mocks.MockMetrics {
	red := gomock.Cond(func(name any) bool {
		text, ok := name.(string)

		return ok && !strings.HasPrefix(text, "resilience_")
	})

	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any()).AnyTimes()

	histogram := mocks.NewMockHistogram(ctrl)
	histogram.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()

	gauge := mocks.NewMockGauge(ctrl)
	gauge.EXPECT().Set(gomock.Any(), gomock.Any()).AnyTimes()

	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().Counter(red, gomock.Any(), gomock.Any()).Return(counter).AnyTimes()
	metrics.EXPECT().Histogram(red, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(histogram).AnyTimes()
	metrics.EXPECT().Gauge(red, gomock.Any(), gomock.Any()).Return(gauge).AnyTimes()

	return metrics
}

// TestObserved_ShedCountedOnceThroughDecorations tests that a call a full channel
// bulkhead sheds is counted once when it runs through Decorations.
//
// Why this test is important:
//   - the shedding layer counts the refusal; a channel bulkhead that also had a
//     recorder would count it again, doubling the rate an operator alerts on.
//
// What it tests:
//   - with a one-slot shedding channel bulkhead, a call made while the slot is held
//     is refused and counts exactly one bulkhead_full rejection.
func TestObserved_ShedCountedOnceThroughDecorations(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	subject := decoratedClient(t, &harness, func(row *decoration.Layers) {
		on := true

		row.Bulkhead.Enabled, row.Bulkhead.Shed = &on, &on
		row.Bulkhead.BulkheadConfig = resilience.DefaultBulkheadConfig()
		row.Bulkhead.BulkheadConfig.MaxConcurrent = 1
	})

	_, err := subject.Exec(context.Background(), observedOp,
		func(ctx context.Context) (any, error) {
			_, shed := subject.Exec(ctx, observedOp,
				func(context.Context) (any, error) { return struct{}{}, nil })
			require.Error(t, shed)

			return struct{}{}, nil
		})
	require.NoError(t, err)

	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "bulkhead_full"), 0)
}

// observedRetries is the end-to-end retry layer's MaxRetries: three attempts in all.
const observedRetries = 2

// decoratedClient returns the client-tier Subject for observedName built by
// Decorations from the default config with tune applied to its override, recording
// into harness's registry (no logger, so the logging layer is off).
func decoratedClient(
	t *testing.T,
	harness *layerHarness,
	tune func(*decoration.Layers),
) decorate.Subject {
	t.Helper()

	cfg := decoration.DefaultConfig()

	cfg.Clients.Overrides[observedName] = fixtures.Build(tune)

	return decorate.NewDecorations(&cfg, fixtures.Build(func(row *decorate.Collaborators) {
		row.Metrics = prom.NewFromRegistry(harness.registry)
	})).
		Subject(client.Tier(), observedName)
}

// observedRetry turns the retry layer on with observedRetries retries 1 ms apart.
func observedRetry(row *decoration.Layers) {
	enabled := true

	row.Retry.Enabled = &enabled
	row.Retry.RetryConfig = resilience.DefaultRetryConfig()
	row.Retry.RetryConfig.MaxRetries = observedRetries
	row.Retry.RetryConfig.InitialInterval = time.Millisecond
	row.Retry.RetryConfig.MaxInterval = time.Millisecond
}

// observedTrip turns the breaker on, tripping on one transient failure and staying
// open for longer than any test runs.
func observedTrip(row *decoration.Layers) {
	enabled := true

	row.CircuitBreaker.Enabled = &enabled
	row.CircuitBreaker.BreakerConfig = resilience.DefaultBreakerConfig()
	row.CircuitBreaker.BreakerConfig.ConsecutiveFailures = 1
	row.CircuitBreaker.BreakerConfig.Timeout = time.Minute
}

// failing returns an operation that fails transiently its first failures calls, then
// succeeds, counting its calls in calls.
func failing(failures int, calls *int) func(context.Context) (struct{}, error) {
	return func(context.Context) (struct{}, error) {
		*calls++
		if *calls <= failures {
			return struct{}{}, apperr.New(apperr.CodeUnavailable, observedDown)
		}

		return struct{}{}, nil
	}
}

// TestObserved_RetriesThroughDecorations tests the retry series a client-tier operation
// exports when it runs through Decorations with retry on.
//
// Why this test is important:
//   - this is the path every client takes in production: config → Decorations → the
//     client tier's chain. The layer tests pin each layer; this pins that the wiring
//     hands the retry layer a recorder under the names an operator queries.
//
// What it tests:
//   - an operation that fails twice then succeeds counts 2 retries under tier
//     "client", the name and the operation, and no exhaustion; one that always fails
//     counts 1 exhaustion.
func TestObserved_RetriesThroughDecorations(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	subject := decoratedClient(t, &harness, observedRetry)
	calls := 0

	_, err := subject.Exec(
		context.Background(),
		observedOp,
		failing(observedTwice, &calls),
	)
	require.NoError(t, err)
	require.InDelta(t, observedTwice, harness.value(t,
		"resilience_retries_total", observedOperation, observedOp), 0)
	require.InDelta(t, 0, harness.value(t,
		"resilience_retry_exhausted_total", observedOperation, observedOp), 0)

	_, err = subject.Exec(context.Background(), observedOp, failing(math.MaxInt, &calls))
	require.Error(t, err)
	require.InDelta(t, 1, harness.value(t,
		"resilience_retry_exhausted_total", observedOperation, observedOp), 0)
}

// TestObserved_BreakerThroughDecorations tests the breaker series a client-tier
// operation exports when it runs through Decorations with the breaker on.
//
// Why this test is important:
//   - a breaker held open is invisible without its state and its rejections; this pins
//     that Decorations builds the breaker with its Observer and the layer with its
//     recorder.
//
// What it tests:
//   - one failure opens the breaker (state 2) and the next call counts one breaker_open
//     rejection without running.
func TestObserved_BreakerThroughDecorations(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	subject := decoratedClient(t, &harness, observedTrip)
	calls := 0

	_, _ = subject.Exec(context.Background(), observedOp, failing(1, &calls))

	_, err := subject.Exec(context.Background(), observedOp, failing(0, &calls))

	require.True(t, circuitbreaker.IsOpen(err))
	require.Equal(t, 1, calls, "the refused call never runs")
	require.InDelta(t, observedTwice, harness.value(t,
		"resilience_breaker_state", textName, observedName), 0)
	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "breaker_open"), 0)
}

// TestObserved_RetryThroughBreakerCountsPerAttempt tests that the client tier counts a
// breaker rejection per attempt, since its breaker sits inside its retry layer.
//
// Why this test is important:
//   - the granularity is documented (ARCHITECTURE.md#resilience-metrics): one client
//     operation can count several breaker_open rejections, and an operator reading the
//     rate must know it counts attempts, not operations.
//
// What it tests:
//   - with retry and breaker on, one failure opens the breaker and each of the two
//     retries is rejected: one operation counts 2 retries and 2 breaker_open
//     rejections, and only the first attempt reaches the operation.
func TestObserved_RetryThroughBreakerCountsPerAttempt(t *testing.T) {
	t.Parallel()

	harness := newObserved(t)
	subject := decoratedClient(t, &harness, func(row *decoration.Layers) {
		observedRetry(row)
		observedTrip(row)
	})
	calls := 0

	_, err := subject.Exec(context.Background(), observedOp, failing(1, &calls))
	require.Error(t, err)
	require.Equal(t, 1, calls, "only the first attempt reaches the operation")
	require.InDelta(t, observedRetries, harness.value(t,
		"resilience_retries_total", observedOperation, observedOp), 0)
	require.InDelta(t, observedRetries, harness.value(t,
		observedRejections, observedReason, "breaker_open"), 0)
}
