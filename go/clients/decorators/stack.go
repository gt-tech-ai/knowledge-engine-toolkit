// Package decorators provides the shared client-boundary resilience stack: one
// reusable builder that composes, from outermost to innermost,
//
//	Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging
//
// around any direct-SDK client operation (S3, SQS, Bedrock, …). It
// generalizes the pattern proven in clients/storage/decorators (which hand-rolled
// CB/Timeout/Tracing/Metrics/Logging): each per-client decorator delegates its
// per-operation work to [Run], so every direct-SDK client gets the same
// protection the RPC/gRPC/repo paths already have.
//
// The layers are nil-able: a nil primitive (or zero timeout) disables that layer,
// so a bare [Stack] is a passthrough. Which layers a client gets is chosen by
// configuration ([StackFromConfig]); resilience primitives come from
// go/foundation/resilience behind go/core/interfaces.
package decorators

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultBuckets returns histogram bucket boundaries (seconds) for operation latency.
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// Stack composes resilience + observability layers around a client boundary. A
// nil layer (or zero timeout) is skipped, so the zero-value Stack is a
// passthrough. Build one with [New] + the With* options, or [StackFromConfig].
type Stack struct {
	// bulkhead bounds concurrent in-flight operations (the outermost layer); nil disables
	// it.
	bulkhead interfaces.Bulkhead

	// retrier retries Retryable operations outside the circuit breaker; nil disables retry.
	retrier interfaces.Retrier

	// cb is the circuit breaker evaluated per attempt; nil disables the breaker.
	cb interfaces.CircuitBreaker

	// tracer emits one span per operation attempt; nil disables tracing.
	tracer oteltrace.Tracer

	// logger records failed operations; nil disables the failure log.
	logger interfaces.Logger

	// ops counts every operation attempt (client_operations_total); nil (via WithMetrics)
	// disables metrics.
	ops interfaces.Counter

	// errs counts failed operation attempts (client_errors_total).
	errs interfaces.Counter

	// dur observes per-operation latency in seconds (client_operation_duration_seconds).
	dur interfaces.Histogram

	// name labels this client in metrics and span names.
	name string

	// timeout is the per-attempt deadline; zero disables the timeout layer.
	timeout time.Duration
}

// New creates a bare (passthrough) Stack labelled name in metrics and spans.
func New(name string) *Stack { return &Stack{name: name} }

// WithBulkhead adds the outermost concurrency-limiting layer.
func (s *Stack) WithBulkhead(b interfaces.Bulkhead) *Stack { s.bulkhead = b; return s }

// WithRetrier adds the Retry layer, applied only to Retryable operations.
func (s *Stack) WithRetrier(r interfaces.Retrier) *Stack { s.retrier = r; return s }

// WithCircuitBreaker adds the circuit-breaker layer, evaluated per attempt.
func (s *Stack) WithCircuitBreaker(
	cb interfaces.CircuitBreaker,
) *Stack {
	s.cb = cb
	return s
}

// WithTimeout sets the per-attempt deadline; zero leaves the timeout layer off.
func (s *Stack) WithTimeout(d time.Duration) *Stack { s.timeout = d; return s }

// WithTracer emits an OpenTelemetry span per operation.
func (s *Stack) WithTracer(t oteltrace.Tracer) *Stack { s.tracer = t; return s }

// WithLogger logs each failed operation with its op name and error.
func (s *Stack) WithLogger(l interfaces.Logger) *Stack { s.logger = l; return s }

// WithMetrics records per-operation counts, errors, and latency. A nil Metrics
// leaves the metrics layer off.
func (s *Stack) WithMetrics(m interfaces.Metrics) *Stack {
	if m == nil {
		return s
	}
	s.ops = m.Counter(
		"client_operations_total",
		"Total client operations",
		"client",
		"op",
	)
	s.errs = m.Counter(
		"client_errors_total",
		"Total client operation errors",
		"client",
		"op",
	)
	s.dur = m.Histogram(
		"client_operation_duration_seconds",
		"Client operation duration in seconds",
		defaultBuckets(),
		"client", "op",
	)
	return s
}

// RunOpts are the per-operation flags for [Run].
type RunOpts struct {
	// Retryable enables the Retry layer for this operation. Body- or
	// stream-consuming and non-idempotent operations (e.g. S3 Upload,
	// CompleteMultipartUpload) must pass false so they are never replayed.
	Retryable bool
}

// Run executes fn through the stack in the order
// Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging.
//
// The nesting is load-bearing: Retry is OUTSIDE the circuit breaker, so each
// retry attempt passes through the breaker (it observes per-attempt outcomes and,
// once open, short-circuits the remaining attempts without invoking fn). The
// bulkhead holds its slot for the whole retry sequence. It is a free function
// because Go methods cannot be generic; the result is captured across attempts
// and the last attempt's value is returned.
func Run[T any](
	ctx context.Context,
	s *Stack,
	op string,
	opts RunOpts,
	fn func(context.Context) (T, error),
) (T, error) {
	var out T

	// Innermost: timeout → tracing → metrics/logging around a single fn call.
	inner := func() error {
		cctx := ctx
		if s.timeout > 0 {
			var cancel context.CancelFunc
			cctx, cancel = context.WithTimeout(ctx, s.timeout)
			defer cancel()
		}
		var span oteltrace.Span
		if s.tracer != nil {
			cctx, span = s.tracer.Start(cctx, "client."+op,
				oteltrace.WithAttributes(attribute.String("client.name", s.name)))
			defer span.End()
		}
		start := time.Now()
		var err error
		out, err = fn(cctx)
		s.record(ctx, op, start, err, span)
		return err
	}

	// CircuitBreaker wraps each attempt.
	run := inner
	if s.cb != nil {
		prev := run
		run = func() error { return s.cb.Execute(prev) }
	}
	// Retry wraps the circuit-broken attempt (outside the CB) for Retryable ops.
	if opts.Retryable && s.retrier != nil {
		prev := run
		run = func() error { return s.retrier.Retry(ctx, prev) }
	}
	// Bulkhead is outermost, bounding concurrency across the whole sequence. The
	// acquire uses the caller's ctx (NOT the per-attempt timeout, which is applied
	// only after a slot is held), so a caller that needs the slot wait bounded must
	// pass a ctx with a deadline. (A dedicated acquire timeout is not yet part of the
	// resilience config surface.)
	if s.bulkhead != nil {
		prev := run
		run = func() error { return s.bulkhead.Execute(ctx, prev) }
	}

	err := run()
	return out, err
}

// RunStream runs fn through Bulkhead → CircuitBreaker → Timeout → Tracing →
// Metrics → Logging for a lazy-stream operation whose returned value must outlive
// this call (e.g. an S3 GetObject body read AFTER the method returns). Unlike
// [Run], the timeout and span are NOT ended on return — the caller must invoke the
// returned cleanup exactly once when the stream is closed, so the deadline stays
// alive for the whole transfer. Retry is never applied to a stream (the body
// cannot be replayed). On error, cleanup is invoked before returning.
func RunStream[T any](
	ctx context.Context,
	s *Stack,
	op string,
	fn func(context.Context) (T, error),
) (val T, cleanup func(), err error) {
	cctx := ctx
	cancel := func() {}
	if s.timeout > 0 {
		cctx, cancel = context.WithTimeout(ctx, s.timeout)
	}
	var span oteltrace.Span
	if s.tracer != nil {
		cctx, span = s.tracer.Start(cctx, "client."+op,
			oteltrace.WithAttributes(attribute.String("client.name", s.name)))
	}

	start := time.Now()
	fetch := func() error {
		var e error
		val, e = fn(cctx)
		return e
	}
	run := fetch
	if s.cb != nil {
		run = func() error { return s.cb.Execute(fetch) }
	}
	if s.bulkhead != nil {
		err = s.bulkhead.Execute(ctx, run)
	} else {
		err = run()
	}
	s.record(ctx, op, start, err, span)

	cleanup = func() {
		cancel()
		if span != nil {
			span.End()
		}
	}
	if err != nil {
		cleanup()
		return val, func() {}, err
	}
	return val, cleanup, nil
}

// record updates metrics, span status, and a failure log for a completed attempt.
// It runs once per attempt, so a retried operation records each attempt separately:
// client_operations_total / client_errors_total count attempts (not logical ops), and
// each failed attempt is logged at Debug — this is an INNER seam, so its failure log is
// suppressed in staging/prod (level=info); only the outermost recovery/transport seam
// logs Error there (ARCHITECTURE.md#decorators: the rule covers the client, repository,
// service, pipeline and workflow stacks; the lock and replay-buffer loggers still log
// at Error).
func (s *Stack) record(
	ctx context.Context,
	op string,
	start time.Time,
	err error,
	span oteltrace.Span,
) {
	if s.ops != nil {
		s.ops.Inc(s.name, op)
		s.dur.Observe(time.Since(start).Seconds(), s.name, op)
		if err != nil {
			s.errs.Inc(s.name, op)
		}
	}
	if err == nil {
		return
	}
	if span != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	if s.logger != nil {
		s.logger.WithContext(ctx).Debug(
			"client operation failed",
			"client",
			s.name,
			"op",
			op,
			"error",
			err.Error(),
		)
	}
}
