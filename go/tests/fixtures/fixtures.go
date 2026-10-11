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

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	m := mocks.NewMockLogger(newCtrl())
	m.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
	m.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()
	m.EXPECT().Warn(gomock.Any(), gomock.Any()).AnyTimes()
	m.EXPECT().Error(gomock.Any(), gomock.Any()).AnyTimes()
	m.EXPECT().With(gomock.Any()).Return(m).AnyTimes()
	m.EXPECT().WithContext(gomock.Any()).Return(m).AnyTimes()
	return m
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

	m := mocks.NewMockMetrics(ctrl)
	m.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).
		AnyTimes()
	m.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).
		AnyTimes()
	m.EXPECT().Gauge(gomock.Any(), gomock.Any(), gomock.Any()).Return(gauge).AnyTimes()
	m.EXPECT().Handler().Return(http.NotFoundHandler()).AnyTimes()
	return m
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

	t := mocks.NewMockTracer(ctrl)
	t.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ string,
			_ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			return ctx, span
		},
	).
		AnyTimes()
	t.EXPECT().Shutdown(gomock.Any()).Return(nil).AnyTimes()
	return t
}

// ---------------------------------------------------------------------------
// Service double (generic MockService[TestEntity, TestParams, string])
// ---------------------------------------------------------------------------

// StubService returns a generated MockService whose Get returns getErr (or panics
// when panicOnGet), and otherwise echoes a canned entity; List/Create/Update/Delete
// succeed. It replaces the former CustomMockService.
func StubService(
	getErr error,
	panicOnGet bool,
) *mocks.MockService[TestEntity, TestParams, string] {
	m := mocks.NewMockService[TestEntity, TestParams, string](newCtrl())
	m.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (*TestEntity, error) {
			if panicOnGet {
				panic("test panic")
			}
			if getErr != nil {
				return nil, getErr
			}
			return &TestEntity{ID: id, Name: "mock"}, nil
		},
	).AnyTimes()
	m.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&types.Page[TestEntity]{}, nil).AnyTimes()
	m.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, e *TestEntity) (*TestEntity, error) { return e, nil },
	).AnyTimes()
	m.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, e *TestEntity) (*TestEntity, error) { return e, nil },
	).
		AnyTimes()
	m.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return m
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

// StubRetrier returns a generated MockRetrier that fails the operation the first
// failUntil attempts (or always, capped at 3, when alwaysFail), plus a getter for
// the number of attempts made. It replaces the former FakeRetrier.
func StubRetrier(
	failUntil int,
	alwaysFail bool,
) (retrier *mocks.MockRetrier, attempts func() int) {
	var count int
	retrier = mocks.NewMockRetrier(newCtrl())
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, op func() error) error {
			maxAttempts := failUntil + 1
			if alwaysFail {
				maxAttempts = 3
			}
			var lastErr error
			for range maxAttempts {
				count++
				lastErr = op()
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

// StubCircuitBreaker returns a generated MockCircuitBreaker: when open, Execute
// rejects without invoking the function; otherwise it runs the function.
func StubCircuitBreaker(open bool) *mocks.MockCircuitBreaker {
	cb := mocks.NewMockCircuitBreaker(newCtrl())
	cb.EXPECT().Execute(gomock.Any()).DoAndReturn(func(fn func() error) error {
		if open {
			return apperr.Sentinel("circuit breaker is open")
		}
		return fn()
	}).AnyTimes()
	return cb
}

// StubRateLimiter returns a generated MockRateLimiter: Allow reports allowed, and
// Wait returns an error when not allowed.
func StubRateLimiter(allowed bool) *mocks.MockRateLimiter {
	rl := mocks.NewMockRateLimiter(newCtrl())
	rl.EXPECT().Allow().Return(allowed).AnyTimes()
	rl.EXPECT().Wait(gomock.Any()).DoAndReturn(func(context.Context) error {
		if !allowed {
			return apperr.Sentinel("rate limit exceeded")
		}
		return nil
	}).AnyTimes()
	return rl
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
	s := mocks.NewMockClientStream(newCtrl())
	s.EXPECT().Header().Return(metadata.MD(nil), nil).AnyTimes()
	s.EXPECT().Trailer().Return(metadata.MD(nil)).AnyTimes()
	s.EXPECT().CloseSend().Return(nil).AnyTimes()
	s.EXPECT().Context().Return(ctx).AnyTimes()
	s.EXPECT().SendMsg(gomock.Any()).Return(sendErr).AnyTimes()
	var recvIdx int
	s.EXPECT().RecvMsg(gomock.Any()).DoAndReturn(func(any) error {
		if recvIdx >= len(recvErrs) {
			return io.EOF
		}
		err := recvErrs[recvIdx]
		recvIdx++
		return err
	}).AnyTimes()
	return s
}

// ---------------------------------------------------------------------------
// Stateful store double (in-memory MockStore[TestEntity, TestParams, string])
// ---------------------------------------------------------------------------

// StubStore returns a generated MockStore backed by an in-memory map, so CRUD
// through it is observable end-to-end (Create then Get returns the entity, Delete
// drops it, Exists reflects presence). It replaces the former CustomMockStore.
func StubStore() *mocks.MockStore[TestEntity, TestParams, string] {
	items := map[string]*TestEntity{}
	s := mocks.NewMockStore[TestEntity, TestParams, string](newCtrl())
	s.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (*TestEntity, error) {
			if e, ok := items[id]; ok {
				return e, nil
			}
			return nil, nil //nolint:nilnil // not-found is (nil, nil) by design
		},
	).AnyTimes()
	s.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
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
	s.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, e *TestEntity) (*TestEntity, error) {
			items[e.ID] = e
			return e, nil
		},
	).
		AnyTimes()
	s.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string, e *TestEntity) (*TestEntity, error) {
			items[id] = e
			return e, nil
		},
	).
		AnyTimes()
	s.EXPECT().Delete(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) error { delete(items, id); return nil },
	).AnyTimes()
	s.EXPECT().Exists(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (bool, error) { _, ok := items[id]; return ok, nil },
	).
		AnyTimes()
	return s
}

// ---------------------------------------------------------------------------
// SpyLogger (call-recording logger backed by a generated MockLogger)
// ---------------------------------------------------------------------------

// SpyLogger records log calls made through an embedded generated MockLogger, for
// tests that assert which messages were logged — directly and through WithContext
// child loggers. It replaces the former hand-rolled mocks.SpyLogger.
type SpyLogger struct {
	// MockLogger is the embedded generated mock whose expectations back the spy.
	*mocks.MockLogger
	// ChildDebugCalls records Debug messages logged on the WithContext child loggers,
	// so a test can assert the child actually logged (e.g. an observability decorator
	// that binds WithContext(ctx) for trace correlation and logs at Debug).
	ChildDebugCalls *[]string
	// ChildInfoCalls records Info messages logged on the WithContext child loggers.
	ChildInfoCalls *[]string
	// ChildWarnCalls records Warn messages logged on the WithContext child loggers.
	ChildWarnCalls *[]string
	// ChildErrorCalls records Error messages logged on the WithContext child loggers.
	ChildErrorCalls *[]string
	// parentDebugRef is non-nil on a child logger; the child's Debug calls also append
	// to the parent's ChildDebugCalls slice through it.
	parentDebugRef *[]string
	// parentInfoRef is non-nil on a child logger; the child's Info calls also append
	// to the parent's ChildInfoCalls slice through it.
	parentInfoRef *[]string
	// parentWarnRef is non-nil on a child logger; the child's Warn calls also append
	// to the parent's ChildWarnCalls slice through it.
	parentWarnRef *[]string
	// parentErrorRef is non-nil on a child logger; the child's Error calls also append
	// to the parent's ChildErrorCalls slice through it.
	parentErrorRef *[]string
	// DebugCalls records Debug messages logged directly on this logger.
	DebugCalls []string
	// InfoCalls records Info messages logged directly on this logger.
	InfoCalls []string
	// WarnCalls records Warn messages logged directly on this logger.
	WarnCalls []string
	// ErrorCalls records Error messages logged directly on this logger.
	ErrorCalls []string
	// ContextUsed is true when this logger was created by a WithContext call.
	ContextUsed bool
}

// Compile-time assertion that *SpyLogger satisfies interfaces.Logger.
var _ interfaces.Logger = (*SpyLogger)(nil)

// NewSpyLogger returns a SpyLogger that records log calls through a generated
// MockLogger, with WithContext yielding a recording child.
func NewSpyLogger() *SpyLogger {
	s := &SpyLogger{
		MockLogger:      mocks.NewMockLogger(newCtrl()),
		ChildDebugCalls: &[]string{},
		ChildInfoCalls:  &[]string{},
		ChildWarnCalls:  &[]string{},
		ChildErrorCalls: &[]string{},
	}
	configureSpyLogger(s)
	return s
}

// configureSpyLogger registers the recording expectations on s's embedded mock.
func configureSpyLogger(s *SpyLogger) {
	m := s.MockLogger
	m.EXPECT().Debug(gomock.Any(), gomock.Any()).Do(func(msg string, _ ...any) {
		s.DebugCalls = append(s.DebugCalls, msg)
		if s.parentDebugRef != nil {
			*s.parentDebugRef = append(*s.parentDebugRef, msg)
		}
	}).AnyTimes()
	m.EXPECT().Info(gomock.Any(), gomock.Any()).Do(func(msg string, _ ...any) {
		s.InfoCalls = append(s.InfoCalls, msg)
		if s.parentInfoRef != nil {
			*s.parentInfoRef = append(*s.parentInfoRef, msg)
		}
	}).AnyTimes()
	m.EXPECT().Warn(gomock.Any(), gomock.Any()).Do(func(msg string, _ ...any) {
		s.WarnCalls = append(s.WarnCalls, msg)
		if s.parentWarnRef != nil {
			*s.parentWarnRef = append(*s.parentWarnRef, msg)
		}
	}).AnyTimes()
	m.EXPECT().Error(gomock.Any(), gomock.Any()).Do(func(msg string, _ ...any) {
		s.ErrorCalls = append(s.ErrorCalls, msg)
		if s.parentErrorRef != nil {
			*s.parentErrorRef = append(*s.parentErrorRef, msg)
		}
	}).AnyTimes()
	m.EXPECT().With(gomock.Any()).Return(s).AnyTimes()
	m.EXPECT().
		WithContext(gomock.Any()).
		DoAndReturn(func(context.Context) interfaces.Logger {
			child := &SpyLogger{
				MockLogger:      mocks.NewMockLogger(newCtrl()),
				ContextUsed:     true,
				parentDebugRef:  s.ChildDebugCalls,
				parentInfoRef:   s.ChildInfoCalls,
				parentWarnRef:   s.ChildWarnCalls,
				parentErrorRef:  s.ChildErrorCalls,
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
