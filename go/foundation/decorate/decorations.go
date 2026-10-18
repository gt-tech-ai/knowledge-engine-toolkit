package decorate

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// Observability carries the collaborators the observability layers use. A nil one
// leaves its layer out, so the zero value observes nothing.
type Observability struct {
	// Logger backs the logging layer.
	Logger interfaces.Logger

	// Metrics backs the metrics layer.
	Metrics interfaces.Metrics

	// Tracer backs the tracing layer.
	Tracer interfaces.Tracer
}

// Collaborators are the runtime pieces a component's layers use; which layers run is
// the decoration config's call.
type Collaborators struct {
	// Observability backs the tracing, metrics and logging layers.
	Observability

	// Auth backs the auth layer: it authorizes an operation by its "<name>.<op>" action.
	Auth func(ctx context.Context, action string) error
}

// Decorations builds every component's Deps from the decoration config: the composition
// root makes one and hands it to each tier. The zero Decorations decorates nothing.
type Decorations struct {
	// cfg is every tier's layer config; nil decorates nothing.
	cfg *decoration.Config

	// collab backs the layers the config turns on.
	collab Collaborators

	// reg records every component resolved and every resolution that failed, shared by
	// the copies of one Decorations; nil (the zero Decorations) records nothing.
	reg *registry
}

// NewDecorations returns the Decorations over a copy of cfg and the collaborators c; a
// nil cfg decorates nothing.
func NewDecorations(cfg *decoration.Config, c Collaborators) Decorations {
	if cfg == nil {
		return Decorations{collab: c, reg: newRegistry()}
	}
	own := *cfg
	return Decorations{cfg: &own, collab: c, reg: newRegistry()}
}

// LoadDecorations reads the decoration section from loader, laid over
// decoration.DefaultConfig, and returns the Decorations over it and c: the one call a
// composition root makes. A section that fails to decode is CodeInvalidInput.
func LoadDecorations(
	loader interfaces.ConfigLoader,
	c Collaborators,
) (Decorations, error) {
	cfg, err := decoration.Load(loader)
	if err != nil {
		return Decorations{}, err
	}
	return NewDecorations(&cfg, c), nil
}

// Observability returns the collaborators' observability, for a component that logs or
// meters outside its layers (a subscriber's receive loop).
func (d Decorations) Observability() Observability { return d.collab.Observability }

// Deps returns the Deps of the component named name in tier: the tier's default layers
// with the overrides of name and its dotted prefixes on top, and records name as used
// for Verify. A tier the config does not know, an override that turns on a layer tier
// does not order, or a layer whose tuning is invalid, is CodeInvalidInput.
func (d Decorations) Deps(tier Tier, name string) (Deps, error) {
	d.reg.use(tier.Name, name)
	cfg := d.cfg
	if cfg == nil {
		cfg = &decoration.Config{}
	}
	tc, ok := cfg.Tier(tier.Name)
	if !ok {
		return Deps{}, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("decoration: no config for tier %q", tier.Name),
		)
	}
	if err := tier.check(name, tc.Overriding(name)); err != nil {
		return Deps{}, err
	}
	return Build(name, tc.For(name), d.collab)
}

// Subject returns the Subject of the component named name in tier, decorated as Deps
// resolves it. A component built where no error can be returned (a pipeline stage, a
// job, a gate) resolves through it: a resolution error is recorded for Verify, and the
// Subject runs with recovery only until boot fails on it.
func (d Decorations) Subject(tier Tier, name string) Subject {
	deps, err := d.Deps(tier, name)
	if err != nil {
		d.reg.fail(err)
		return tier.Subject(name, Deps{})
	}
	return tier.Subject(name, deps)
}

// Build returns the Deps of the component named name from its resolved layers: each
// layer that is on gets its collaborator from c, or its primitive built from its tuning
// (the breaker is named name). An invalid tuning is CodeInvalidInput.
func Build(name string, l decoration.Layers, c Collaborators) (Deps, error) {
	deps := Deps{SkipRecovery: !decoration.On(l.Recovery)}
	if !deps.SkipRecovery {
		deps.PanicLogger = c.Logger
	}
	if decoration.On(l.Logging) {
		deps.Logger = c.Logger
	}
	if decoration.On(l.Metrics) {
		deps.Metrics = c.Metrics
	}
	if decoration.On(l.Tracing) {
		deps.Tracer = c.Tracer
	}
	if decoration.On(l.Auth) {
		deps.Auth = c.Auth
	}
	if l.Timeout != nil {
		deps.Timeout = *l.Timeout
	}
	var err error
	if decoration.On(l.Retry.Enabled) {
		if deps.Retrier, err = newRetrier(l.Retry); err != nil {
			return Deps{}, wrapLayer(err, name, "retry")
		}
	}
	if decoration.On(l.CircuitBreaker.Enabled) {
		if deps.Breaker, err = newBreaker(name, l.CircuitBreaker); err != nil {
			return Deps{}, wrapLayer(err, name, "circuit_breaker")
		}
	}
	if decoration.On(l.Bulkhead.Enabled) {
		if deps.Bulkhead, err = newBulkhead(l.Bulkhead); err != nil {
			return Deps{}, wrapLayer(err, name, "bulkhead")
		}
		deps.ShedLoad = decoration.On(l.Bulkhead.Shed)
	}
	if decoration.On(l.RateLimit.Enabled) {
		if deps.RateLimiter, err = newRateLimiter(l.RateLimit); err != nil {
			return Deps{}, wrapLayer(err, name, "rate_limit")
		}
	}
	return deps, nil
}

// newRetrier builds the exponential-backoff retrier r tunes.
func newRetrier(r decoration.Retry) (interfaces.Retrier, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      r.MaxRetries,
		InitialInterval: r.InitialInterval,
		MaxInterval:     r.MaxInterval,
		Multiplier:      r.Multiplier,
		MaxElapsedTime:  r.MaxElapsedTime,
	})
}

// newBreaker builds the breaker named name that b tunes.
func newBreaker(
	name string,
	b decoration.CircuitBreaker,
) (interfaces.CircuitBreaker, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                name,
		MaxRequests:         b.MaxRequests,
		Interval:            b.Interval,
		Timeout:             b.Timeout,
		ConsecutiveFailures: b.ConsecutiveFailures,
		FailureRatio:        b.FailureRatio,
		MinRequests:         b.MinRequests,
	})
}

// newBulkhead builds the bulkhead b tunes; its kind is "channel" (or empty) or
// "adaptive".
func newBulkhead(b decoration.Bulkhead) (interfaces.Bulkhead, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	var kind bulkhead.Kind
	switch b.Kind {
	case "", "channel":
		kind = bulkhead.KindChannel
	case "adaptive":
		kind = bulkhead.KindAdaptive
	default:
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"unknown bulkhead kind %q (want channel|adaptive)", b.Kind,
		))
	}
	return bulkhead.NewFromConfig(bulkhead.Config{
		Kind:              kind,
		MaxConcurrent:     b.MaxConcurrent,
		MinConcurrent:     b.MinConcurrent,
		InitialConcurrent: b.InitialConcurrent,
		RTTThreshold:      b.RTTThreshold,
		BackoffRatio:      b.BackoffRatio,
	})
}

// newRateLimiter builds the token-bucket limiter r tunes.
func newRateLimiter(r decoration.RateLimit) (interfaces.RateLimiter, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return ratelimiter.New(
		ratelimiter.KindToken,
		ratelimiter.WithRate(r.Rate),
		ratelimiter.WithBurst(r.Burst),
	)
}

// wrapLayer codes a layer build failure for the component named name.
func wrapLayer(err error, name, layer string) error {
	return apperr.Wrap(err, apperr.CodeInvalidInput, "decoration "+name+": "+layer)
}
