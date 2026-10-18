// Package client is the client tier: the one decoration every outbound client (an
// SDK, a datastore, an HTTP API) runs its operations through. Tier orders the layers;
// the decoration config (decoration.clients, built by decorate.Decorations) switches and
// tunes them per client and per operation; Base, built from the client's contract, gives
// its decorated wrapper typed per-operation helpers as generic methods; and Kinds is each
// family's backend table.
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

// Base runs one client's operations through the client tier: each operation is one
// method of the client's contract, decorated as the client's name plus the method's
// snake_case name. Its embedded Subject supplies Name, Exec (an idempotent operation,
// retried when the tier retries) and ExecOnce (one that must never be replayed); Do,
// DoOnce, Stream and Open cover the rest. The zero Base runs every operation
// undecorated.
type Base struct {
	// Subject runs the client's operations; it supplies Name, Exec and ExecOnce.
	decorate.Subject
}

// Option shapes how a Base decorates its client.
type Option func(*options)

// options is a Base's decoration shape.
type options struct {
	// adjust edits every operation's Deps (Handler, AtMostOnce).
	adjust []func(decorate.Deps) decorate.Deps

	// tier is the client tier, its span kind set by Producer or Handler.
	tier decorate.Tier
}

// Producer decorates a message publisher: the client tier with producer spans, since
// each operation sends a message to a broker.
func Producer() Option {
	return func(o *options) { o.tier.SpanKind = interfaces.SpanKindProducer }
}

// Handler decorates a consumer's message handlers: observed (in consumer spans) and
// bounded, never retried or broken — the broker's redelivery is the retry, and one
// poison message must not trip the others.
func Handler() Option {
	return func(o *options) {
		o.tier.SpanKind = interfaces.SpanKindConsumer
		o.adjust = append(o.adjust, func(d decorate.Deps) decorate.Deps {
			d = d.WithoutRetry()
			d.Breaker, d.Bulkhead = nil, nil
			return d
		})
	}
}

// AtMostOnce never retries the client's operations, whatever the config says: a retry
// could repeat a delivery (a Redis PUBLISH).
func AtMostOnce() Option {
	return func(o *options) {
		o.adjust = append(o.adjust, decorate.Deps.WithoutRetry)
	}
}

// newOptions applies opts over the plain client tier.
func newOptions(opts []Option) options {
	o := options{tier: Tier()}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// NewBase returns the Base for the client named name whose operations are the methods
// of its contract C, decorated from decs: the client as the client-tier component name,
// and each operation op as name+"."+op. A C that is not an interface, or a decoration
// that fails to resolve, is an error.
func NewBase[C any](
	decs decorate.Decorations,
	name string,
	opts ...Option,
) (Base, error) {
	o := newOptions(opts)
	subject, err := decs.SubjectOf[C](o.tier, name, o.adjust...)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
}

// NewWireBase returns the Base for the client named name whose operations are wire-level
// names known only per call (an RPC procedure, an HTTP route, a job kind): every
// operation runs on the client's own chain, labeled by its name, so the config tunes
// the client as a whole.
func NewWireBase(decs decorate.Decorations, name string, opts ...Option) (Base, error) {
	o := newOptions(opts)
	subject, err := decs.NewSubject(o.tier, name, nil, o.adjust...)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
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
	_, err := b.ExecOnce(ctx, op, unit(fn))
	return err
}

// Stream opens a stream as the operation op. The open is decorated (never retried);
// the operation's timeout bounds the stream's whole life, so the returned release must
// be called when the stream is closed. On error release is already done.
func (b Base) Stream[R any](
	ctx context.Context,
	op string,
	open func(context.Context) (R, error),
) (stream R, release func(), err error) {
	release = func() {}
	if timeout := b.Timeout(op); timeout > 0 {
		ctx, release = context.WithTimeout(ctx, timeout)
	}
	stream, err = b.Streaming().Exec(ctx, op, open)
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
	timeout := b.Timeout(op)
	if timeout <= 0 {
		return b.Streaming().Exec(ctx, op, open)
	}
	return b.Streaming().Exec(ctx, op, func(ctx context.Context) (R, error) {
		return openWithin(ctx, timeout, op, open)
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
