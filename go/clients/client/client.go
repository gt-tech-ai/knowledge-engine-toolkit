// Package client is the client tier: the one decoration every outbound client (an
// SDK, a datastore, an HTTP API) runs its operations through. Tier orders the layers;
// the decoration config (decoration.clients, built by decorate.Decorations) switches and
// tunes them per client; Base gives a client's decorated wrapper typed, per-operation
// helpers as generic methods; and Kinds is each family's backend table.
package client

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the client decoration tier, outermost first: the bulkhead bounds the calls in
// flight, retry replays a transient failure through the breaker, and each attempt gets
// its own deadline, client span, metrics and log line.
func Tier() decorate.Tier {
	return decorate.Tier{
		Name:     "client",
		SpanKind: interfaces.SpanKindClient,
		Order: []decorate.Layer{
			decorate.LayerBulkhead,
			decorate.LayerRetry,
			decorate.LayerCircuitBreaker,
			decorate.LayerTimeout,
			decorate.LayerTracing,
			decorate.LayerMetrics,
			decorate.LayerLogging,
		},
	}
}

// Base runs one client's operations through the client tier. Its embedded Exec runs
// an idempotent operation (retried when the tier retries); Once, Do and DoOnce cover
// the rest. The zero Base runs every operation undecorated.
type Base struct {
	// Subject runs idempotent operations; it supplies Name and Exec.
	decorate.Subject

	// once runs operations that must never be replayed.
	once decorate.Subject

	// stream opens streams: never retried, its deadline owned by Stream.
	stream decorate.Subject

	// ops resolves each named call's decoration beneath the client's name.
	ops decorate.Scope

	// timeout bounds a stream's whole life (open through close).
	timeout time.Duration
}

// NewBase returns the Base for the client named name over deps.
func NewBase(name string, deps decorate.Deps) Base {
	return newBase(Tier(), name, deps)
}

// NewProducerBase returns the Base for a message publisher named name over deps: the
// client tier with producer spans, since each operation sends a message to a broker.
func NewProducerBase(name string, deps decorate.Deps) Base {
	return newBase(withSpanKind(interfaces.SpanKindProducer), name, deps)
}

// newBase builds the Base for the client named name over deps in tier t.
func newBase(t decorate.Tier, name string, deps decorate.Deps) Base {
	streamDeps := deps.WithoutRetry()
	streamDeps.Timeout = 0
	return Base{
		Subject: t.Subject(name, deps),
		once:    t.Subject(name, deps.WithoutRetry()),
		stream:  t.Subject(name, streamDeps),
		timeout: deps.Timeout,
		ops:     decorate.NewScope(t, name, deps),
	}
}

// NewBaseFrom returns the Base for the client named name, decorated from decs: the
// client as the client-tier component name, and each named call op as name+"."+op. A
// decoration that fails to resolve is CodeInvalidInput.
func NewBaseFrom(decs decorate.Decorations, name string) (Base, error) {
	deps, err := decs.Deps(Tier(), name)
	if err != nil {
		return Base{}, err
	}
	b := newBase(Tier(), name, deps)
	b.ops = decs.Scope(Tier(), name)
	return b, nil
}

// Call is the client's named idempotent call: the SDK function fn, shaped as an AWS SDK
// v2 operation (ctx, input, ...option), runs through the client tier with its retry,
// breaker, bulkhead and timeout. fn takes no options here; its client was configured
// when it was built.
func Call[In, Out, O any](
	b Base,
	op string,
	fn func(ctx context.Context, in In, opts ...O) (Out, error),
) decorate.OpFunc[In, Out] {
	return decorate.Operation(
		b.ops.Subject(op),
		func(ctx context.Context, in In) (Out, error) {
			return fn(ctx, in)
		},
	)
}

// CallOnce is Call without the retry layer, for a call that is not idempotent (a send, a
// create) or that consumes its input.
func CallOnce[In, Out, O any](
	b Base,
	op string,
	fn func(ctx context.Context, in In, opts ...O) (Out, error),
) decorate.OpFunc[In, Out] {
	return decorate.Operation(
		b.ops.Subject(op, decorate.Deps.WithoutRetry),
		func(ctx context.Context, in In) (Out, error) { return fn(ctx, in) },
	)
}

// withSpanKind is the client tier with spans of kind.
func withSpanKind(kind interfaces.SpanKind) decorate.Tier {
	t := Tier()
	t.SpanKind = kind
	return t
}

// NewHandlerBase returns the Base for a consumer's message handlers, named name, over
// deps without its retry, breaker and bulkhead: a handler is observed (in consumer
// spans) and bounded, never retried or broken — the broker's redelivery is the retry,
// and one poison message must not trip the others.
func NewHandlerBase(name string, deps decorate.Deps) Base {
	deps = deps.WithoutRetry()
	deps.Breaker, deps.Bulkhead = nil, nil
	return newBase(withSpanKind(interfaces.SpanKindConsumer), name, deps)
}

// Once runs fn as the operation op without the retry layer: an operation that is not
// idempotent or consumes its input (an upload body, a create).
func (b Base) Once[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	return b.once.Exec(ctx, op, fn)
}

// Do runs the error-only idempotent operation op.
func (b Base) Do(ctx context.Context, op string, fn func(context.Context) error) error {
	_, err := b.Exec(ctx, op, unit(fn))
	return err
}

// DoOnce runs the error-only operation op without the retry layer.
func (b Base) DoOnce(
	ctx context.Context,
	op string,
	fn func(context.Context) error,
) error {
	_, err := b.once.Exec(ctx, op, unit(fn))
	return err
}

// Stream opens a stream as the operation op. The open is decorated (never retried);
// the client's timeout bounds the stream's whole life, so the returned release must be
// called when the stream is closed. On error release is already done.
func (b Base) Stream[R any](
	ctx context.Context,
	op string,
	open func(context.Context) (R, error),
) (stream R, release func(), err error) {
	release = func() {}
	if b.timeout > 0 {
		ctx, release = context.WithTimeout(ctx, b.timeout)
	}
	stream, err = b.stream.Exec(ctx, op, open)
	if err != nil {
		release()
		var zero R
		return zero, func() {}, err
	}
	return stream, release, nil
}

// Open opens a long-lived stream as the operation op, bounding only the open: the
// client's timeout caps how long open may take, while the stream it returns lives on
// ctx with no deadline of the client's (a generation stream can outlast any call
// budget). The open is decorated (never retried); an open that overruns is abandoned —
// its context canceled — and returned as CodeTimeout.
func (b Base) Open[R any](
	ctx context.Context,
	op string,
	open func(context.Context) (R, error),
) (R, error) {
	if b.timeout <= 0 {
		return b.stream.Exec(ctx, op, open)
	}
	return b.stream.Exec(ctx, op, func(ctx context.Context) (R, error) {
		return openWithin(ctx, b.timeout, op, open)
	})
}

// opened is one open's outcome, handed from its goroutine to openWithin.
type opened[R any] struct {
	// stream is the opened stream (zero on failure).
	stream R
	// err is the open's error.
	err error
}

// openWithin runs open on a cancelable (deadline-free) child of ctx and waits at most
// timeout for it: on success the child context becomes the stream's for its whole life;
// on failure or overrun it is canceled, tearing down the abandoned open.
func openWithin[R any](
	ctx context.Context,
	timeout time.Duration,
	op string,
	open func(context.Context) (R, error),
) (R, error) {
	openCtx, abort := context.WithCancel(ctx)
	done := make(chan opened[R], 1)
	go func() {
		stream, err := open(openCtx)
		done <- opened[R]{stream: stream, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			abort()
			return res.stream, res.err
		}
		context.AfterFunc(ctx, abort) // release the child once the parent ends
		return res.stream, nil
	case <-timer.C:
		abort()
		var zero R
		return zero, apperr.New(
			apperr.CodeTimeout,
			op+" open timed out after "+timeout.String(),
		)
	}
}

// unit adapts an error-only operation to Exec's typed shape.
func unit(fn func(context.Context) error) func(context.Context) (struct{}, error) {
	return func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }
}
