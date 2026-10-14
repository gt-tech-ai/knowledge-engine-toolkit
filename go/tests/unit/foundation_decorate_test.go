package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// recorderMW is a test OpMiddleware that appends "<name>:before"/"<name>:after"
// around next, so a test can assert the composed call order.
type recorderMW struct {
	log  *[]string
	name string
}

func (m recorderMW) WrapOp(
	ctx context.Context,
	_ string,
	next func(context.Context) error,
) error {
	*m.log = append(*m.log, m.name+":before")
	err := next(ctx)
	*m.log = append(*m.log, m.name+":after")
	return err
}

// TestDecorateExec_ArbitraryReturnTypes tests that Exec[R] decorates an operation of
// ANY return type and returns fn's natural typed result
//
// Why this test is important:
//   - The whole point of the generalization is to drop the (*T,error) shoehorn: a
//     custom op returning bool / []*int / struct{} must decorate and return its own
//     type with no closure-capture boilerplate. This pins that Exec[R] carries the
//     typed result out of the type-erased middleware core.
//
// What it tests:
//   - Exec[bool] / Exec[[]*int] / Exec[struct{}] each run fn through the chain and
//     return the natural typed value + propagate the error.
func TestDecorateExec_ArbitraryReturnTypes(t *testing.T) {
	t.Parallel()
	mw := decorate.Chain() // empty chain: pure passthrough

	b, err := decorate.Exec(context.Background(), mw, "IsOwned",
		func(context.Context) (bool, error) { return true, nil })
	require.NoError(t, err)
	assert.True(t, b)

	xs := []*int{new(int)}
	got, err := decorate.Exec(context.Background(), mw, "List",
		func(context.Context) ([]*int, error) { return xs, nil })
	require.NoError(t, err)
	assert.Equal(t, xs, got)

	_, err = decorate.Exec(context.Background(), mw, "Update",
		func(context.Context) (struct{}, error) { return struct{}{}, nil })
	require.NoError(t, err)
}

// TestDecorateExec_ReturnsResultUnmodifiedOnError tests the Exec[R] contract that a
// non-nil error alongside a non-zero result is propagated verbatim — no zeroing.
//
// Why this test is important:
//   - Callers must get exactly what fn returned so a partial result + error survives
//     the decoration; zeroing R on error would silently drop data a caller returned.
//
// What it tests:
//   - Exec returns (42, err) when fn returns (42, err).
func TestDecorateExec_ReturnsResultUnmodifiedOnError(t *testing.T) {
	t.Parallel()
	want := apperr.Sentinel("boom")
	got, err := decorate.Exec(context.Background(), decorate.Chain(), "X",
		func(context.Context) (int, error) { return 42, want })
	require.ErrorIs(t, err, want)
	assert.Equal(t, 42, got, "Exec must not zero the result on error")
}

// TestDecorateChain_OutermostFirst tests that Chain composes middlewares
// outermost-first.
//
// Why this test is important:
//   - The per-tier chains rely on a deterministic nesting order (recovery outermost;
//     Tracing → Metrics → Logging). If Chain applied middlewares in the wrong order,
//     the behavior-preserving guarantee would silently break.
//
// What it tests:
//   - Chain(a, b) runs a:before, b:before, fn, b:after, a:after (a outermost).
func TestDecorateChain_OutermostFirst(t *testing.T) {
	t.Parallel()
	var log []string
	mw := decorate.Chain(
		recorderMW{name: "a", log: &log},
		recorderMW{name: "b", log: &log},
	)
	_, err := decorate.Exec(context.Background(), mw, "op",
		func(context.Context) (struct{}, error) {
			log = append(log, "fn")
			return struct{}{}, nil
		})
	require.NoError(t, err)
	assert.Equal(t, []string{"a:before", "b:before", "fn", "b:after", "a:after"}, log)
}

// TestTracingMW_OpensSpanWithTierAttrs tests that the tracing middleware opens a span
// tagged with the tier's subject/operation attribute keys.
//
// Why this test is important:
//   - The unified tracing middleware serves both tiers by parameterizing the attr
//     keys; if it dropped or mis-keyed them, custom-op spans would lose the
//     repo/service identification the CRUD spans carry.
//
// What it tests:
//   - a repository-tier tracing MW sets repository.name=<name> and
//     repository.operation=<op>.
func TestTracingMW_OpensSpanWithTierAttrs(t *testing.T) {
	t.Parallel()
	attrs := map[string]any{}
	tracer := fixtures.SpyTracer(func(k string, v any) { attrs[k] = v })
	mw := decorate.NewTracing(
		tracer,
		"repository",
		"documents",
		interfaces.SpanKindInternal,
	)

	_, err := decorate.Exec(context.Background(), mw, "UpdateStatus",
		func(context.Context) (struct{}, error) { return struct{}{}, nil })
	require.NoError(t, err)
	assert.Equal(t, "documents", attrs["repository.name"])
	assert.Equal(t, "UpdateStatus", attrs["repository.operation"])
}

// TestMetricsMW_RecordsCountAndError tests that the metrics middleware records an
// operation count + duration and, on failure, an error count.
//
// Why this test is important:
//   - Custom ops must be measured like CRUD ops. This pins the count/duration on the
//     success path and the additional error count on failure, with the tier labels.
//
// What it tests:
//   - success → ops.Inc(name, op) + dur.Observe(_, name, op), no error count;
//     failure → additionally errs.Inc(name, op).
func TestMetricsMW_RecordsCountAndError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockMetrics(ctrl)
	ops := mocks.NewMockCounter(ctrl)
	errs := mocks.NewMockCounter(ctrl)
	dur := mocks.NewMockHistogram(ctrl)
	m.EXPECT().
		Counter("repository_operations_total", gomock.Any(), "repository", "operation").
		Return(ops)
	m.EXPECT().
		Counter("repository_errors_total", gomock.Any(), "repository", "operation").
		Return(errs)
	m.EXPECT().
		Histogram(
			"repository_operation_duration_seconds",
			gomock.Any(),
			gomock.Any(),
			"repository",
			"operation",
		).
		Return(dur)

	mw := decorate.NewMetrics(m, "repository", "documents")

	ops.EXPECT().Inc("documents", "UpdateStatus").Times(2)
	dur.EXPECT().Observe(gomock.Any(), "documents", "UpdateStatus").Times(2)
	errs.EXPECT().Inc("documents", "UpdateStatus").Times(1) // only the failing call

	_, err := decorate.Exec(context.Background(), mw, "UpdateStatus",
		func(context.Context) (struct{}, error) { return struct{}{}, nil })
	require.NoError(t, err)
	_, err = decorate.Exec(
		context.Background(),
		mw,
		"UpdateStatus",
		func(context.Context) (struct{}, error) { return struct{}{}, apperr.Sentinel("boom") },
	)
	require.Error(t, err)
}

// TestLoggingMW_LogsOnEntryAndFailure tests that the logging middleware logs at Debug
// on the context (child) logger for entry and failure.
//
// Why this test is important:
//   - The access trail for custom ops must survive the generalization, at Debug on the
//     WithContext child (so trace ids correlate), matching the prior decorator.
//
// What it tests:
//   - a failing op yields two child Debug entries (entry + "<op> failed").
func TestLoggingMW_LogsOnEntryAndFailure(t *testing.T) {
	t.Parallel()
	spy := fixtures.NewSpyLogger()
	mw := decorate.NewLogging(spy, "repository", "documents")

	_, err := decorate.Exec(
		context.Background(),
		mw,
		"UpdateStatus",
		func(context.Context) (struct{}, error) { return struct{}{}, apperr.Sentinel("boom") },
	)
	require.Error(t, err)
	assert.Len(
		t,
		*spy.ChildDebugCalls,
		2,
		"entry + failure Debug lines on the context logger",
	)
}

// TestTimeoutMW_AppliesDeadline tests that the timeout middleware bounds the operation
// with a deadline.
//
// Why this test is important:
//   - Custom ops must inherit the per-op timeout; if the deadline were dropped, a slow
//     custom op could hang a request indefinitely.
//
// What it tests:
//   - inside next, the context carries a deadline; a zero timeout is a passthrough.
func TestTimeoutMW_AppliesDeadline(t *testing.T) {
	t.Parallel()
	var hadDeadline bool
	_, err := decorate.Exec(
		context.Background(),
		decorate.NewTimeout(50*time.Millisecond),
		"op",
		func(ctx context.Context) (struct{}, error) {
			_, hadDeadline = ctx.Deadline()
			return struct{}{}, nil
		},
	)
	require.NoError(t, err)
	assert.True(t, hadDeadline, "timeout MW must set a context deadline")

	var passthrough bool
	_, _ = decorate.Exec(context.Background(), decorate.NewTimeout(0), "op",
		func(ctx context.Context) (struct{}, error) {
			_, passthrough = ctx.Deadline()
			return struct{}{}, nil
		})
	assert.False(t, passthrough, "a zero timeout is a passthrough (no deadline)")
}

// TestRetryMW_TxGuardedRunsOnce tests that the retry middleware runs the operation
// exactly once inside a transaction (no retry) and uses the retrier outside one.
//
// Why this test is important:
//   - Retrying a statement inside the caller's transaction is unsafe (the tx is
//     already poisoned). This pins the exact InTx guard the former repo retry
//     decorator enforced — the single most important behavior to preserve.
//
// What it tests:
//   - InTx(ctx): the retrier is never called and next runs once;
//   - not InTx: the retrier drives next (here twice, simulating one retry).
func TestRetryMW_TxGuardedRunsOnce(t *testing.T) {
	t.Parallel()

	t.Run("inside a tx runs once, retrier not used", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mr := mocks.NewMockRetrier(ctrl) // strict: any Retry call fails the test
		calls := 0
		_, err := decorate.Exec(interfaces.WithInTx(context.Background()),
			decorate.NewRetry(mr), "UpdateStatus",
			func(context.Context) (struct{}, error) { calls++; return struct{}{}, nil })
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "inside a tx the op runs exactly once (no retry)")
	})

	t.Run("outside a tx the retrier drives the op", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		mr := mocks.NewMockRetrier(ctrl)
		mr.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, op func() error) error {
				_ = op()    // attempt 1
				return op() // attempt 2 (a retry)
			},
		)
		calls := 0
		_, err := decorate.Exec(context.Background(), decorate.NewRetry(mr), "List",
			func(context.Context) (struct{}, error) { calls++; return struct{}{}, nil })
		require.NoError(t, err)
		assert.Equal(t, 2, calls, "outside a tx the retrier re-runs the op")
	})
}

// TestCircuitBreakerMW_ShortCircuitsWhenOpen tests that the circuit-breaker middleware
// does not run the operation when the breaker rejects.
//
// Why this test is important:
//   - An open breaker must shed load without touching the datastore; if the middleware
//     ran the op anyway, the breaker would provide no protection.
//
// What it tests:
//   - when cb.Execute rejects without invoking fn, next never runs and the error
//     propagates.
func TestCircuitBreakerMW_ShortCircuitsWhenOpen(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cb := mocks.NewMockCircuitBreaker(ctrl)
	open := apperr.Sentinel("circuit open")
	cb.EXPECT().Execute(gomock.Any()).Return(open) // rejects without calling fn

	ran := false
	_, err := decorate.Exec(context.Background(), decorate.NewCircuitBreaker(cb), "Get",
		func(context.Context) (struct{}, error) { ran = true; return struct{}{}, nil })
	require.ErrorIs(t, err, open)
	assert.False(t, ran, "an open breaker must not run the operation")
}

// TestRecoveryMW_ConvertsPanic tests that the recovery middleware converts a panic in
// the operation into a coded error.
//
// Why this test is important:
//   - A panic in a custom service op must not unwind the server; recovery is the
//     outermost service middleware for exactly this.
//
// What it tests:
//   - a panicking op yields a non-nil error and no escaping panic.
func TestRecoveryMW_ConvertsPanic(t *testing.T) {
	t.Parallel()
	_, err := decorate.Exec(context.Background(),
		decorate.NewRecovery(fixtures.NopLogger(), "service", "users"), "Provision",
		func(context.Context) (struct{}, error) { panic("boom") })
	require.Error(t, err, "a panic must be recovered into an error")
}

// TestAuthMW_ShortCircuitsOnDeny tests that the auth middleware denies before running
// the operation and allows otherwise.
//
// Why this test is important:
//   - authMW is the required eighth middleware: a custom service op must be authorized
//     like CRUD ops. Dropping the check would be a security regression.
//
// What it tests:
//   - a denying authFn short-circuits (next not run, error propagated) with the action
//     "<name>.<op>"; an allowing authFn runs next.
func TestAuthMW_ShortCircuitsOnDeny(t *testing.T) {
	t.Parallel()

	var gotAction string
	deny := apperr.Sentinel("forbidden")
	ran := false
	_, err := decorate.Exec(context.Background(),
		decorate.NewAuth("users", func(_ context.Context, action string) error {
			gotAction = action
			return deny
		}), "Provision",
		func(context.Context) (struct{}, error) { ran = true; return struct{}{}, nil })
	require.ErrorIs(t, err, deny)
	assert.False(t, ran, "a denied op must not run")
	assert.Equal(t, "users.Provision", gotAction)

	ranOK := false
	_, err = decorate.Exec(
		context.Background(),
		decorate.NewAuth(
			"users",
			func(context.Context, string) error { return nil },
		),
		"Provision",
		func(context.Context) (struct{}, error) { ranOK = true; return struct{}{}, nil },
	)
	require.NoError(t, err)
	assert.True(t, ranOK, "an allowed op runs")
}

// TestTierChain_AppliesTheTiersLayersInOrderAndSkipsMissingOnes tests how a Tier turns
// its Order and Deps into a chain.
//
// Why this test is important:
//   - Every request tier (pipeline, workflow, service, repository) decorates through
//     Tier.Chain, so its order is each tier's documented decorator order; a layer run in
//     the wrong place (a retry outside the breaker, auth after the timeout) changes what
//     is retried, shed or bounded.
//
// What it tests:
//   - The layers whose collaborators are set run outermost first in Order (tracing →
//     logging → auth → breaker → retry here), each seeing the next as its inner call.
//   - A layer with no collaborator is left out, and the zero Subject runs undecorated.
func TestTierChain_AppliesTheTiersLayersInOrderAndSkipsMissingOnes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	var calls []string
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End()
	tracer.EXPECT().Start(gomock.Any(), "orders.Place", gomock.Any()).DoAndReturn(
		func(
			ctx context.Context, _ string, _ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			calls = append(calls, "tracing")
			return ctx, span
		})
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger)
	logger.EXPECT().Debug("service.Place", gomock.Any()).Do(func(string, ...any) {
		calls = append(calls, "logging")
	})
	breaker := mocks.NewMockCircuitBreaker(ctrl)
	breaker.EXPECT().Execute(gomock.Any()).DoAndReturn(func(fn func() error) error {
		calls = append(calls, "breaker")
		return fn()
	})
	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, op func() error) error {
			calls = append(calls, "retry")
			return op()
		})
	tier := decorate.Tier{Name: "service", Order: []decorate.Layer{
		decorate.LayerRecovery, decorate.LayerTracing, decorate.LayerMetrics,
		decorate.LayerLogging, decorate.LayerAuth, decorate.LayerTimeout,
		decorate.LayerCircuitBreaker, decorate.LayerRetry,
	}}
	subject := tier.Subject("orders", decorate.Deps{
		Observability: decorate.Observability{Tracer: tracer, Logger: logger},
		Auth: func(_ context.Context, action string) error {
			calls = append(calls, "auth:"+action)
			return nil
		},
		Retrier: retrier,
		Breaker: breaker,
	})
	assert.Equal(t, "orders", subject.Name())

	got, err := subject.Exec(t.Context(), "Place", func(context.Context) (int, error) {
		calls = append(calls, "op")
		return 7, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, got)
	assert.Equal(
		t,
		[]string{
			"tracing",
			"logging",
			"auth:orders.Place",
			"breaker",
			"retry",
			"op",
		},
		calls,
	)

	got, err = decorate.Subject{}.Exec(t.Context(), "Plain",
		func(context.Context) (int, error) { return 1, nil })
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

// TestLoggingMW_EdgeTierLogsFailuresAtWarnOrError tests the edge tier's failure log.
//
// Why this test is important:
//   - A REST handler's failure is logged by no one else: if the controller tier logged it
//     at Debug, like an inner tier, a 500 would leave no trace in a production log.
//
// What it tests:
//   - On the controller tier (Edge) a not-found failure logs at Warn and an internal one
//     at Error; an inner tier logs the same failure at Debug.
func TestLoggingMW_EdgeTierLogsFailuresAtWarnOrError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().Debug("controller.Get", gomock.Any()).Times(2)
	logger.EXPECT().Warn("controller.Get failed", gomock.Any())
	logger.EXPECT().Error("controller.Get failed", gomock.Any())
	logger.EXPECT().Debug("service.Get", gomock.Any())
	logger.EXPECT().Debug("service.Get failed", gomock.Any())
	deps := decorate.Deps{
		Observability: decorate.Observability{Logger: logger},
		SkipRecovery:  true,
	}
	fail := func(code apperr.ErrorCode) func(context.Context) (int, error) {
		return func(context.Context) (int, error) { return 0, apperr.New(code, "x") }
	}

	edge := transport.Tier().Subject("users", deps)
	_, err := edge.Exec(t.Context(), "Get", fail(apperr.CodeNotFound))
	require.Error(t, err)
	_, err = edge.Exec(t.Context(), "Get", fail(apperr.CodeInternal))
	require.Error(t, err)
	_, err = service.Tier().Subject("users", deps).
		Exec(t.Context(), "Get", fail(apperr.CodeInternal))
	require.Error(t, err)
}
