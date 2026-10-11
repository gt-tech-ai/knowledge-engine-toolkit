package fixtures_test

import (
	"context"
	"errors"
	"io"
	"testing"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestFactories_ExerciseEveryDouble smoke-tests every factory through its
// interface, including variadic calls with zero AND multiple args.
//
// Why this test is important:
//   - The factory package is the shared dependency of ~1000 converted call sites
//     across 11 modules; a bad gomock variadic matcher here would fail all of them.
//
// What it tests:
//   - Each factory's returned generated mock honours its interface contract (no-op
//     loggers/metrics/tracers, echoing/erroring/panicking pipelines, the retrier's
//     attempt counting, circuit-breaker gating, and stream Recv sequencing).
func TestFactories_ExerciseEveryDouble(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Logger: zero and multi variadic, plus With/WithContext chaining.
	l := fixtures.NopLogger()
	l.Debug("m")
	l.Info("m", "k", "v")
	l.With("svc", "x").Warn("w")
	l.WithContext(ctx).Error("e")

	// Metrics: Counter/Histogram/Gauge with zero and multi labels.
	m := fixtures.NopMetrics()
	c := m.Counter("n", "h")
	c.Inc()
	c.Add(1, "lbl")
	m.Counter("n", "h", "lbl").Inc("a")
	m.Histogram("n", "h", nil).Observe(1)
	g := m.Gauge("n", "h")
	g.Set(1)
	g.Inc()
	g.Dec()
	_ = m.Handler()

	// Tracer no-op + spy capture.
	tr := fixtures.NopTracer()
	_, sp := tr.Start(ctx, "op")
	sp.SetAttribute("k", "v")
	sp.End()
	_ = tr.Shutdown(ctx)

	var gotKey string
	spy := fixtures.SpyTracer(func(k string, _ any) { gotKey = k })
	_, s2 := spy.Start(ctx, "op")
	s2.SetAttribute("attr", 1)
	if gotKey != "attr" {
		t.Fatalf("SpyTracer: got key %q, want attr", gotKey)
	}

	// Service: canned entity, error, and panic paths.
	if got, _ := fixtures.StubService(nil, false).
		Get(ctx, "1"); got == nil ||
		got.Name != "mock" {
		t.Fatalf("StubService.Get: %+v, want Name=mock", got)
	}
	boom := apperr.Sentinel("boom")
	if _, err := fixtures.StubService(boom, false).Get(ctx, "1"); !errors.Is(err, boom) {
		t.Fatalf("StubService(err).Get: %v, want boom", err)
	}
	assertPanics(t, func() { _, _ = fixtures.StubService(nil, true).Get(ctx, "1") })

	// Pipeline: pass/err/panic.
	if got, _ := fixtures.PassPipeline().Execute(ctx, "x"); got != "mock-output-x" {
		t.Fatalf("PassPipeline: %q", got)
	}
	if _, err := fixtures.ErrPipeline(boom).Execute(ctx, "x"); !errors.Is(err, boom) {
		t.Fatalf("ErrPipeline: %v", err)
	}
	assertPanics(t, func() { _, _ = fixtures.PanicPipeline().Execute(ctx, "x") })

	// Retrier: fails twice then succeeds → 3 attempts.
	retrier, attempts := fixtures.StubRetrier(2, false)
	calls := 0
	_ = retrier.Retry(ctx, func() error {
		calls++
		if calls <= 2 {
			return boom
		}
		return nil
	})
	if attempts() != 3 {
		t.Fatalf("StubRetrier attempts: %d, want 3", attempts())
	}

	// Circuit breaker: open rejects, closed runs.
	if err := fixtures.StubCircuitBreaker(true).
		Execute(func() error { return nil }); err == nil {
		t.Fatal("open circuit breaker should reject")
	}
	ran := false
	_ = fixtures.StubCircuitBreaker(false).
		Execute(func() error { ran = true; return nil })
	if !ran {
		t.Fatal("closed circuit breaker should run fn")
	}

	// Rate limiter.
	if !fixtures.StubRateLimiter(true).Allow() {
		t.Fatal("allowed rate limiter should allow")
	}
	if fixtures.StubRateLimiter(false).Allow() {
		t.Fatal("denied rate limiter should deny")
	}

	// ClientStream: recvErrs in order then io.EOF; Context echoes.
	cs := fixtures.StubClientStream(ctx, nil, boom)
	if cs.Context() != ctx {
		t.Fatal("StubClientStream.Context should echo ctx")
	}
	if err := cs.RecvMsg(nil); !errors.Is(err, boom) {
		t.Fatalf("RecvMsg #1: %v, want boom", err)
	}
	if err := cs.RecvMsg(nil); !errors.Is(err, io.EOF) {
		t.Fatalf("RecvMsg #2: %v, want EOF", err)
	}

	// StubStore: in-memory CRUD is observable end-to-end.
	store := fixtures.StubStore()
	created, err := store.Create(ctx, &fixtures.TestEntity{ID: "e1", Name: "alice"})
	if err != nil || created.Name != "alice" {
		t.Fatalf("StubStore.Create: %+v, %v", created, err)
	}
	if got, _ := store.Get(ctx, "e1"); got == nil || got.Name != "alice" {
		t.Fatalf("StubStore.Get after Create: %+v", got)
	}
	if ok, _ := store.Exists(ctx, "e1"); !ok {
		t.Fatal("StubStore.Exists after Create should be true")
	}
	_ = store.Delete(ctx, "e1")
	if got, _ := store.Get(ctx, "e1"); got != nil {
		t.Fatalf("StubStore.Get after Delete: %+v, want nil", got)
	}

	// SpyLogger: records direct calls and WithContext child calls.
	spyLog := fixtures.NewSpyLogger()
	spyLog.Error("direct-err")
	spyLog.Debug("dbg")
	child := spyLog.WithContext(ctx)
	child.Error("child-err")
	if len(spyLog.ErrorCalls) != 1 || spyLog.ErrorCalls[0] != "direct-err" {
		t.Fatalf("SpyLogger.ErrorCalls: %v", spyLog.ErrorCalls)
	}
	if len(spyLog.DebugCalls) != 1 || spyLog.DebugCalls[0] != "dbg" {
		t.Fatalf("SpyLogger.DebugCalls: %v", spyLog.DebugCalls)
	}
	if len(*spyLog.ChildErrorCalls) != 1 || (*spyLog.ChildErrorCalls)[0] != "child-err" {
		t.Fatalf("SpyLogger.ChildErrorCalls: %v", *spyLog.ChildErrorCalls)
	}
}

// assertPanics fails the test if fn does not panic.
func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
