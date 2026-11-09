// Package fixtures provides thin, hand-written factories over the GENERATED
// gomock mocks in the sibling mocks package. It is the shared test-support layer:
// the mocks package stays generated-only (mock_*.go), and every configured double
// a test needs is built here from those generated mocks.
//
// # Why the factories take no *testing.T
//
// The no-op doubles (NopLogger/NopMetrics/NopTracer) build their gomock controller
// from a no-op TestReporter, so callers get a ready no-op mock with zero ceremony —
// `fixtures.NopLogger()` drops in wherever `mocks.NopLogger{}` used to. Every method
// is registered AnyTimes, so an unmet/absent expectation can never fire and the
// controller never needs Finish().
package fixtures

import (
	"context"
	"io"
	"net/http"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// noopReporter is a gomock.TestReporter that discards every report, letting the
// no-op factories build a controller without a *testing.T. Safe only because every
// expectation the factories register is AnyTimes, so no report is ever emitted.
type noopReporter struct{}

// Errorf discards the report (no-op).
func (noopReporter) Errorf(string, ...any) {}

// Fatalf discards the report (no-op); it does not abort, unlike *testing.T.Fatalf.
func (noopReporter) Fatalf(string, ...any) {}

// newCtrl returns a controller bound to the no-op reporter (no *testing.T needed).
func newCtrl() *gomock.Controller { return gomock.NewController(noopReporter{}) }

// ---------------------------------------------------------------------------
// Test domain types (generic type parameters for the generic mocks)
// ---------------------------------------------------------------------------

// TestEntity is a minimal domain entity for exercising repository and service
// operations through MockStore/MockService generic instantiations.
type TestEntity struct {
	// ID is the entity identifier.
	ID string
	// Name is a human-readable label.
	Name string
}

// TestParams is a query-parameter type for testing List operations.
type TestParams struct{}

// ---------------------------------------------------------------------------
// No-op observability doubles (generated mocks, all-AnyTimes)
// ---------------------------------------------------------------------------

// NopLogger returns a generated MockLogger whose every method is a no-op and whose
// With/WithContext return the same logger, for tests that must pass a logger but
// never assert on log output.
func NopLogger() interfaces.Logger {
	logger := mocks.NewMockLogger(newCtrl())
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
	logger.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()
	logger.EXPECT().Warn(gomock.Any(), gomock.Any()).AnyTimes()
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).AnyTimes()
	logger.EXPECT().With(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()

	return logger
}

// NopMetrics returns a generated MockMetrics whose Counter/Histogram/Gauge yield
// no-op sub-metrics, for tests that must pass a metrics provider but never assert
// on recorded values.
func NopMetrics() interfaces.Metrics {
	ctrl := newCtrl()

	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any()).AnyTimes()
	counter.EXPECT().Add(gomock.Any(), gomock.Any()).AnyTimes()

	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()

	gauge := mocks.NewMockGauge(ctrl)
	gauge.EXPECT().Set(gomock.Any(), gomock.Any()).AnyTimes()
	gauge.EXPECT().Inc(gomock.Any()).AnyTimes()
	gauge.EXPECT().Dec(gomock.Any()).AnyTimes()

	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).
		AnyTimes()
	metrics.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).
		AnyTimes()
	metrics.EXPECT().
		Gauge(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(gauge).
		AnyTimes()
	metrics.EXPECT().Handler().Return(http.NotFoundHandler()).AnyTimes()

	return metrics
}

// NopTracer returns a generated MockTracer whose spans discard every operation.
func NopTracer() interfaces.Tracer { return SpyTracer(nil) }

// SpyTracer returns a generated MockTracer whose span forwards each SetAttribute
// call to onSetAttribute (nil = discard), for tests that assert which span
// attributes were set.
func SpyTracer(onSetAttribute func(key string, value any)) interfaces.Tracer {
	ctrl := newCtrl()

	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().End().AnyTimes()
	span.EXPECT().
		SetAttribute(gomock.Any(), gomock.Any()).
		Do(func(key string, value any) {
			if onSetAttribute != nil {
				onSetAttribute(key, value)
			}
		}).
		AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()

	tracer := mocks.NewMockTracer(ctrl)
	tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ string,
			_ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			return ctx, span
		},
	).
		AnyTimes()
	tracer.EXPECT().Shutdown(gomock.Any()).Return(nil).AnyTimes()

	return tracer
}

// ---------------------------------------------------------------------------
// Service double (generic MockService[TestEntity, TestParams, string])
// ---------------------------------------------------------------------------

// StubService returns a generated MockService whose Get returns getErr, and otherwise
// echoes a canned entity; List/Create/Update/Delete succeed.
func StubService(getErr error) *mocks.MockService[TestEntity, TestParams, string] {
	return stubServiceWithGet(func(identifier string) (*TestEntity, error) {
		if getErr != nil {
			return nil, getErr
		}

		return &TestEntity{ID: identifier, Name: "mock"}, nil
	})
}

// StubServicePanickingOnGet returns a StubService whose Get panics.
func StubServicePanickingOnGet() *mocks.MockService[TestEntity, TestParams, string] {
	return stubServiceWithGet(func(string) (*TestEntity, error) { panic("test panic") })
}

// stubServiceWithGet returns the generated MockService answering Get with get.
func stubServiceWithGet(
	get func(identifier string) (*TestEntity, error),
) *mocks.MockService[TestEntity, TestParams, string] {
	service := mocks.NewMockService[TestEntity, TestParams, string](newCtrl())
	service.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, identifier string) (*TestEntity, error) {
			return get(identifier)
		},
	).AnyTimes()
	service.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&types.Page[TestEntity]{}, nil).AnyTimes()
	service.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, e *TestEntity) (*TestEntity, error) { return e, nil },
	).AnyTimes()
	service.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, e *TestEntity) (*TestEntity, error) { return e, nil },
	).
		AnyTimes()
	service.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	return service
}

// ---------------------------------------------------------------------------
// Pipeline / Workflow doubles (MockPipeline/MockWorkflow[string, string])
// ---------------------------------------------------------------------------

// StubPipeline returns a generated MockPipeline whose Execute runs exec.
func StubPipeline(
	exec func(ctx context.Context, in string) (string, error),
) *mocks.MockPipeline[string, string] {
	p := mocks.NewMockPipeline[string, string](newCtrl())
	p.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(exec).AnyTimes()

	return p
}

// PassPipeline returns a MockPipeline that echoes "mock-output-"+input.
func PassPipeline() *mocks.MockPipeline[string, string] {
	return StubPipeline(
		func(_ context.Context, in string) (string, error) { return "mock-output-" + in, nil },
	)
}

// ErrPipeline returns a MockPipeline whose Execute returns ("", err).
func ErrPipeline(err error) *mocks.MockPipeline[string, string] {
	return StubPipeline(func(context.Context, string) (string, error) { return "", err })
}

// PanicPipeline returns a MockPipeline whose Execute panics (for recovery-decorator
// tests).
func PanicPipeline() *mocks.MockPipeline[string, string] {
	return StubPipeline(
		func(context.Context, string) (string, error) { panic("test panic in pipeline") },
	)
}

// StubWorkflow returns a generated MockWorkflow whose Execute runs exec.
func StubWorkflow(
	exec func(ctx context.Context, in string) (string, error),
) *mocks.MockWorkflow[string, string] {
	w := mocks.NewMockWorkflow[string, string](newCtrl())
	w.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(exec).AnyTimes()

	return w
}

// PassWorkflow returns a MockWorkflow that echoes "mock-workflow-output-"+input.
func PassWorkflow() *mocks.MockWorkflow[string, string] {
	return StubWorkflow(
		func(_ context.Context, in string) (string, error) {
			return "mock-workflow-output-" + in, nil
		},
	)
}

// ErrWorkflow returns a MockWorkflow whose Execute returns ("", err).
func ErrWorkflow(err error) *mocks.MockWorkflow[string, string] {
	return StubWorkflow(func(context.Context, string) (string, error) { return "", err })
}

// PanicWorkflow returns a MockWorkflow whose Execute panics (for recovery-decorator
// tests).
func PanicWorkflow() *mocks.MockWorkflow[string, string] {
	return StubWorkflow(
		func(context.Context, string) (string, error) { panic("test panic in workflow") },
	)
}

// ---------------------------------------------------------------------------
// Resilience doubles (MockRetrier / MockCircuitBreaker / MockRateLimiter)
// ---------------------------------------------------------------------------

// StubRetrier returns a generated MockRetrier that retries the operation up to
// failUntil times, plus a getter for the number of attempts made.
func StubRetrier(
	failUntil int,
) (retrier *mocks.MockRetrier, attempts func() int) {
	var count int

	retrier = mocks.NewMockRetrier(newCtrl())
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, operation func() error) error {
			var lastErr error

			for range failUntil + 1 {
				count++

				lastErr = operation()
				if lastErr == nil {
					return nil
				}
			}

			return lastErr
		},
	).AnyTimes()

	attempts = func() int { return count }

	return retrier, attempts
}

// OpenCircuitBreaker returns a generated MockCircuitBreaker whose Execute rejects
// without invoking the function.
func OpenCircuitBreaker() *mocks.MockCircuitBreaker {
	circuitBreaker := mocks.NewMockCircuitBreaker(newCtrl())
	circuitBreaker.EXPECT().
		Execute(gomock.Any()).
		Return(apperr.New(apperr.CodeUnavailable, "circuit breaker is open")).
		AnyTimes()

	return circuitBreaker
}

// ClosedCircuitBreaker returns a generated MockCircuitBreaker whose Execute runs the
// function.
func ClosedCircuitBreaker() *mocks.MockCircuitBreaker {
	circuitBreaker := mocks.NewMockCircuitBreaker(newCtrl())
	circuitBreaker.EXPECT().
		Execute(gomock.Any()).
		DoAndReturn(func(fn func() error) error { return fn() }).
		AnyTimes()

	return circuitBreaker
}

// AllowingRateLimiter returns a generated MockRateLimiter that admits every request.
func AllowingRateLimiter() *mocks.MockRateLimiter {
	rateLimiter := mocks.NewMockRateLimiter(newCtrl())
	rateLimiter.EXPECT().Allow().Return(true).AnyTimes()
	rateLimiter.EXPECT().Wait(gomock.Any()).Return(nil).AnyTimes()

	return rateLimiter
}

// DenyingRateLimiter returns a generated MockRateLimiter that refuses every request:
// Allow is false and Wait returns an error.
func DenyingRateLimiter() *mocks.MockRateLimiter {
	rateLimiter := mocks.NewMockRateLimiter(newCtrl())
	rateLimiter.EXPECT().Allow().Return(false).AnyTimes()
	rateLimiter.EXPECT().
		Wait(gomock.Any()).
		Return(apperr.Sentinel("rate limit exceeded")).
		AnyTimes()

	return rateLimiter
}

// ---------------------------------------------------------------------------
// grpc.ClientStream double
// ---------------------------------------------------------------------------

// StubClientStream returns a generated MockClientStream for stream-interceptor
// tests: Context returns ctx (the caller's, never nil), SendMsg returns sendErr, and
// RecvMsg yields recvErrs in order then io.EOF. It replaces the former FakeClientStream.
func StubClientStream(
	ctx context.Context,
	sendErr error,
	recvErrs ...error,
) grpc.ClientStream {
	clientStream := mocks.NewMockClientStream(newCtrl())
	clientStream.EXPECT().Header().Return(metadata.MD(nil), nil).AnyTimes()
	clientStream.EXPECT().Trailer().Return(metadata.MD(nil)).AnyTimes()
	clientStream.EXPECT().CloseSend().Return(nil).AnyTimes()
	clientStream.EXPECT().Context().Return(ctx).AnyTimes()
	clientStream.EXPECT().SendMsg(gomock.Any()).Return(sendErr).AnyTimes()

	var recvIdx int

	clientStream.EXPECT().RecvMsg(gomock.Any()).DoAndReturn(func(any) error {
		if recvIdx >= len(recvErrs) {
			return io.EOF
		}

		err := recvErrs[recvIdx]
		recvIdx++

		return err
	}).AnyTimes()

	return clientStream
}

// ---------------------------------------------------------------------------
// Stateful store double (in-memory MockStore[TestEntity, TestParams, string])
// ---------------------------------------------------------------------------

// StubStore returns a generated MockStore backed by an in-memory map, so CRUD
// through it is observable end-to-end (Create then Get returns the entity, Delete
// drops it, Exists reflects presence). It replaces the former CustomMockStore.
func StubStore() *mocks.MockStore[TestEntity, TestParams, string] {
	items := map[string]*TestEntity{}
	store := mocks.NewMockStore[TestEntity, TestParams, string](newCtrl())
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (*TestEntity, error) {
			if e, ok := items[id]; ok {
				return e, nil
			}

			return nil, nil //nolint:nilnil // not-found is (nil, nil) by design
		},
	).AnyTimes()
	store.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			_ TestParams,
			_ types.PageRequest,
		) (*types.Page[TestEntity], error) {
			out := make([]TestEntity, 0, len(items))
			for _, e := range items {
				out = append(out, *e)
			}

			return &types.Page[TestEntity]{Items: out, Total: int64(len(out))}, nil
		},
	).
		AnyTimes()
	store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, e *TestEntity) (*TestEntity, error) {
			items[e.ID] = e

			return e, nil
		},
	).
		AnyTimes()
	store.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string, e *TestEntity) (*TestEntity, error) {
			items[id] = e

			return e, nil
		},
	).
		AnyTimes()
	store.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(

		func(_ context.Context, id string) error {
			delete(items, id)

			return nil
		},
	).AnyTimes()
	store.EXPECT().Exists(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (bool, error) {
			_, ok := items[id]

			return ok, nil
		},
	).
		AnyTimes()

	return store
}

// ---------------------------------------------------------------------------
// SpyLogger (call-recording logger backed by a generated MockLogger)
// ---------------------------------------------------------------------------

// Compile-time assertion that *SpyLogger satisfies interfaces.Logger.
var _ interfaces.Logger = (*SpyLogger)(nil)

// NewSpyLogger returns a SpyLogger that records log calls through a generated
// MockLogger, with WithContext yielding a recording child.
func NewSpyLogger() *SpyLogger {
	spyLogger := &SpyLogger{
		MockLogger:      mocks.NewMockLogger(newCtrl()),
		ChildDebugCalls: &[]string{},
		ChildInfoCalls:  &[]string{},
		ChildWarnCalls:  &[]string{},
		ChildErrorCalls: &[]string{},
	}
	configureSpyLogger(spyLogger)

	return spyLogger
}

// configureSpyLogger registers the recording expectations on s's embedded mock.
func configureSpyLogger(spyLogger *SpyLogger) {
	logger := spyLogger.MockLogger

	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).
		Do(spyRecorder(&spyLogger.DebugCalls, spyLogger.parentDebugRef)).AnyTimes()
	logger.EXPECT().Info(gomock.Any(), gomock.Any()).
		Do(spyRecorder(&spyLogger.InfoCalls, spyLogger.parentInfoRef)).AnyTimes()
	logger.EXPECT().Warn(gomock.Any(), gomock.Any()).
		Do(spyRecorder(&spyLogger.WarnCalls, spyLogger.parentWarnRef)).AnyTimes()
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).
		Do(spyRecorder(&spyLogger.ErrorCalls, spyLogger.parentErrorRef)).AnyTimes()
	logger.EXPECT().With(gomock.Any()).Return(spyLogger).AnyTimes()
	logger.EXPECT().
		WithContext(gomock.Any()).
		DoAndReturn(func(context.Context) interfaces.Logger {
			child := &SpyLogger{
				MockLogger:      mocks.NewMockLogger(newCtrl()),
				ContextUsed:     true,
				parentDebugRef:  spyLogger.ChildDebugCalls,
				parentInfoRef:   spyLogger.ChildInfoCalls,
				parentWarnRef:   spyLogger.ChildWarnCalls,
				parentErrorRef:  spyLogger.ChildErrorCalls,
				ChildDebugCalls: &[]string{},
				ChildInfoCalls:  &[]string{},
				ChildWarnCalls:  &[]string{},
				ChildErrorCalls: &[]string{},
			}
			configureSpyLogger(child)

			return child
		}).
		AnyTimes()
}

// spyRecorder appends each message to calls and, for a child logger, to its parent's
// list.
func spyRecorder(calls, parent *[]string) func(string, ...any) {
	return func(msg string, _ ...any) {
		*calls = append(*calls, msg)
		if parent != nil {
			*parent = append(*parent, msg)
		}
	}
}
