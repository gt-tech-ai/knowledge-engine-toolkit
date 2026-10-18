package decorate

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Layer names one decoration layer of a tier.
type Layer int

const (
	// LayerRecovery turns a panic into an INTERNAL error; it needs no collaborator.
	LayerRecovery Layer = iota + 1

	// LayerTracing opens a span per operation (Deps.Tracer).
	LayerTracing

	// LayerMetrics records the tier's RED series (Deps.Metrics).
	LayerMetrics

	// LayerLogging logs each operation (Deps.Logger).
	LayerLogging

	// LayerAuth authorizes "<name>.<op>" (Deps.Auth).
	LayerAuth

	// LayerTimeout bounds each operation (Deps.Timeout).
	LayerTimeout

	// LayerCircuitBreaker sheds calls while the breaker is open (Deps.Breaker).
	LayerCircuitBreaker

	// LayerRetry retries transient failures outside a transaction (Deps.Retrier).
	LayerRetry

	// LayerBulkhead bounds the operations in flight (Deps.Bulkhead): it waits for a slot,
	// or, with Deps.ShedLoad, rejects at once when full.
	LayerBulkhead

	// LayerRateLimit rejects operations over the limiter's rate (Deps.RateLimiter).
	LayerRateLimit
)

// Tier is one request tier's decoration: its name (the metric, log and span-attribute
// prefix) and its layers, outermost first. Each tier package declares its Tier as data.
type Tier struct {
	// Name is the tier label ("pipeline", "workflow", "service", "repository", "job",
	// "client").
	Name string

	// Order lists the tier's layers, outermost first.
	Order []Layer

	// SpanKind is the kind of the tracing layer's spans; the zero value is internal,
	// and the clients tier's is client (an outbound call).
	SpanKind interfaces.SpanKind

	// Edge marks the outermost seam of a request (the controllers tier): its logging
	// layer logs a failure once, at Warn for a client fault and at Error otherwise,
	// where an inner tier logs it at Debug.
	Edge bool
}

// Deps carries the collaborators a tier's layers use. A nil collaborator, or a zero
// Timeout, leaves its layer out; recovery needs none and is applied wherever the tier
// orders it unless SkipRecovery is set. Decorations.Deps builds a Deps from config.
type Deps struct {
	// Observability backs the logging, metrics and tracing layers.
	Observability

	// Auth authorizes an operation by its "<name>.<op>" action.
	Auth func(ctx context.Context, action string) error

	// Retrier backs the retry layer.
	Retrier interfaces.Retrier

	// Breaker backs the circuit-breaker layer.
	Breaker interfaces.CircuitBreaker

	// Bulkhead backs the bulkhead layer.
	Bulkhead interfaces.Bulkhead

	// RateLimiter backs the rate-limit layer.
	RateLimiter interfaces.RateLimiter

	// PanicLogger logs a recovered panic (its value and stack); nil falls back to
	// Logger. Build sets it whenever recovery is on, so a panic is logged even in a
	// tier whose logging layer is off.
	PanicLogger interfaces.Logger

	// Timeout bounds each operation.
	Timeout time.Duration

	// SkipRecovery leaves the recovery layer out (the config turned it off).
	SkipRecovery bool

	// ShedLoad makes the bulkhead reject an operation when it is full instead of
	// waiting for a slot (a server sheds; a client queues).
	ShedLoad bool
}

// panicLogger is the logger a recovered panic is logged to: PanicLogger, else Logger.
func (d Deps) panicLogger() interfaces.Logger {
	if d.PanicLogger != nil {
		return d.PanicLogger
	}
	return d.Logger
}

// WithoutRetry returns d with no retrier, for an operation that is not idempotent (a
// create whose commit may have landed).
func (d Deps) WithoutRetry() Deps {
	d.Retrier = nil
	return d
}

// Chain builds the tier's middleware chain for the subject named name from d.
func (t Tier) Chain(name string, d Deps) OpMiddleware {
	mws := make([]OpMiddleware, 0, len(t.Order))
	for _, layer := range t.Order {
		if mw := t.Middleware(layer, name, d); mw != nil {
			mws = append(mws, mw)
		}
	}
	return Chain(mws...)
}

// Middleware returns the middleware for one of the tier's layers over the subject named
// name, or nil when d lacks its collaborator. A protocol adapter (a server's
// interceptors) walks Order with it, standing in its own interceptor for a layer it
// implements natively.
func (t Tier) Middleware(layer Layer, name string, d Deps) OpMiddleware {
	switch layer {
	case LayerRecovery:
		return when(
			!d.SkipRecovery,
			func() OpMiddleware { return NewRecovery(d.panicLogger(), t.Name, name) },
		)
	case LayerTracing:
		return when(
			d.Tracer != nil,
			func() OpMiddleware { return NewTracing(d.Tracer, t.Name, name, t.SpanKind) },
		)
	case LayerMetrics:
		return when(
			d.Metrics != nil,
			func() OpMiddleware { return NewMetrics(d.Metrics, t.Name, name) },
		)
	case LayerLogging:
		return when(
			d.Logger != nil,
			func() OpMiddleware {
				return loggingMW{logger: d.Logger, tier: t.Name, name: name, edge: t.Edge}
			},
		)
	case LayerAuth:
		return when(d.Auth != nil, func() OpMiddleware { return NewAuth(name, d.Auth) })
	case LayerTimeout:
		return when(d.Timeout > 0, func() OpMiddleware { return NewTimeout(d.Timeout) })
	case LayerCircuitBreaker:
		return when(
			d.Breaker != nil,
			func() OpMiddleware { return NewCircuitBreaker(d.Breaker) },
		)
	case LayerRetry:
		return when(d.Retrier != nil, func() OpMiddleware { return NewRetry(d.Retrier) })
	case LayerBulkhead:
		return when(
			d.Bulkhead != nil,
			func() OpMiddleware { return NewBulkhead(d.Bulkhead, d.ShedLoad) },
		)
	case LayerRateLimit:
		return when(
			d.RateLimiter != nil,
			func() OpMiddleware { return NewRateLimit(d.RateLimiter) },
		)
	default:
		return nil
	}
}

// when builds the middleware only when its collaborator is present.
func when(present bool, build func() OpMiddleware) OpMiddleware {
	if !present {
		return nil
	}
	return build()
}

// Subject builds the Subject that runs every operation of the subject named name through
// the tier's chain over d: a pipeline stage, a job or a gate, whose one operation is
// named where it is built.
func (t Tier) Subject(name string, d Deps) Subject {
	return Subject{all: t.chains(name, d), name: name}
}

// chains builds the chains one operation can run on from d.
func (t Tier) chains(name string, d Deps) chains {
	streaming := d.WithoutRetry()
	streaming.Timeout = 0
	return chains{
		full:      t.Chain(name, d),
		once:      t.Chain(name, d.WithoutRetry()),
		observed:  t.Chain(name, Deps{Observability: d.Observability}),
		streaming: t.Chain(name, streaming),
		timeout:   d.Timeout,
	}
}

// chains are one operation's middleware chains: as configured, with the retry layer off
// (a write that must not replay), and with the observability layers only (a statement
// inside a caller's transaction, which must not time out, break or retry on its own).
type chains struct {
	// full is the chain as configured.
	full OpMiddleware

	// once is full without the retry layer.
	once OpMiddleware

	// observed is the tracing, metrics and logging layers only.
	observed OpMiddleware

	// streaming is full without the retry and timeout layers, for opening a stream whose
	// deadline the caller owns.
	streaming OpMiddleware

	// timeout is the operation's configured deadline (zero: none).
	timeout time.Duration
}

// pick returns the chain v selects.
func (c chains) pick(v view) OpMiddleware {
	switch v {
	case viewOnce:
		return c.once
	case viewObserved:
		return c.observed
	case viewStreaming:
		return c.streaming
	case viewFull:
		return c.full
	}
	return c.full
}

// view selects which of an operation's chains a Subject runs it on.
type view int

const (
	// viewFull runs an operation on its chain as configured.
	viewFull view = iota

	// viewOnce runs an operation without the retry layer.
	viewOnce

	// viewObserved runs an operation with the observability layers only.
	viewObserved

	// viewStreaming runs an operation without the retry and timeout layers.
	viewStreaming
)

// Subject runs one component's operations through its tier's chains. Built by
// Decorations.NewSubject from the component's contract, it holds one set of chains per
// operation, each resolved from that operation's own config key ("<name>.<op>"), and an
// operation the contract does not have fails. Built by Tier.Subject (a pipeline stage, a
// job, a gate), one set of chains serves every operation. It is embeddable: a type that
// embeds it gets Name, Exec and ExecOnce. The zero Subject runs operations undecorated.
type Subject struct {
	// ops maps each declared operation to its chains; nil runs every operation on all.
	ops map[string]chains

	// all are the component's chains, for a Subject with no declared operations.
	all chains

	// name is the component's dotted name, the label of every chain.
	name string

	// view selects the chain each operation runs on.
	view view
}

// Name returns the component's name.
func (s Subject) Name() string { return s.name }

// Once returns s running every operation without its retry layer, for a write whose
// commit may have landed (a create, a claim).
func (s Subject) Once() Subject {
	s.view = viewOnce
	return s
}

// Observed returns s running every operation with its observability layers only, for a
// statement that runs inside a caller's transaction.
func (s Subject) Observed() Subject {
	s.view = viewObserved
	return s
}

// Component returns s running any operation on the component's own chains, labeled by
// its name: for an operation named outside the contract (a background loop's pass, named
// by its loop), which the config tunes with the component.
func (s Subject) Component() Subject {
	s.ops = nil
	return s
}

// Streaming returns s running every operation without its retry and timeout layers, for
// opening a stream whose deadline the caller sets from Timeout.
func (s Subject) Streaming() Subject {
	s.view = viewStreaming
	return s
}

// Timeout returns the deadline configured for the operation op (zero when none, or when
// s does not declare op).
func (s Subject) Timeout(op string) time.Duration {
	if s.ops == nil {
		return s.all.timeout
	}
	return s.ops[op].timeout
}

// Exec runs fn as the operation op through its chain and returns its result unmodified.
// On a Subject built from a contract, an op the contract does not have is CodeInternal
// and fn does not run.
func (s Subject) Exec[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	c := s.all
	if s.ops != nil {
		var ok bool
		if c, ok = s.ops[op]; !ok {
			var zero R
			return zero, apperr.New(
				apperr.CodeInternal,
				"decorate: "+s.name+" has no operation "+op,
			)
		}
	}
	mw := c.pick(s.view)
	if mw == nil {
		return fn(ctx)
	}
	return Exec(ctx, mw, op, fn)
}

// ExecOnce runs fn as the operation op without its retry layer.
func (s Subject) ExecOnce[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	return s.Once().Exec(ctx, op, fn)
}
