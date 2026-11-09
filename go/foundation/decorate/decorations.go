package decorate

import (
	"context"
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	// collab backs the layers the config turns on; nil (the zero Decorations) backs none.
	// It is held by pointer so a Decorations stays small enough to pass by value.
	collab *Collaborators

	// reg records every component resolved and every resolution that failed, shared by
	// the copies of one Decorations; nil (the zero Decorations) records nothing.
	reg *registry

	// fixed, when set, is the Deps every name resolves to (Fixed).
	fixed *Deps
}

// Fixed returns the Decorations that resolves every component and operation to d,
// whatever its name: a test builds a base over chosen collaborators with it.
// d must not be nil.
func Fixed(dRef *Deps) Decorations {
	d := *dRef

	return Decorations{
		reg:    newRegistry(),
		fixed:  &d,
		collab: &Collaborators{Observability: d.Observability, Auth: nil},
	}
}

// NewDecorations returns the Decorations over a copy of cfg and the collaborators c; a
// nil cfg decorates nothing.
func NewDecorations(cfg *decoration.Config, collaborators Collaborators) Decorations {
	if cfg == nil {
		return Decorations{collab: &collaborators, reg: newRegistry()}
	}

	own := *cfg

	return Decorations{cfg: &own, collab: &collaborators, reg: newRegistry()}
}

// LoadDecorations reads the decoration section from loader, laid over
// decoration.DefaultConfig, and returns the Decorations over it and c: the one call a
// composition root makes. A section that fails to decode is CodeInvalidInput.
func LoadDecorations(
	loader interfaces.ConfigLoader,
	collaborators Collaborators,
) (Decorations, error) {
	cfg, err := decoration.Load(loader)
	if err != nil {
		return Decorations{}, err
	}

	return NewDecorations(&cfg, collaborators), nil
}

// Observability returns the collaborators' observability, for a component that logs or
// meters outside its layers (a subscriber's receive loop).
func (d Decorations) Observability() Observability {
	return d.collaborators().Observability
}

// Deps returns the Deps of the component named name in tier: the tier's default layers
// with the overrides of name and its dotted prefixes on top, and records name as used
// for Verify. A tier the config does not know, an override that turns on a layer tier
// does not order, or a layer whose tuning is invalid, is CodeInvalidInput.
func (d Decorations) Deps(tier Tier, name string) (Deps, error) {
	d.reg.use(tier.Name, name)

	if d.fixed != nil {
		return *d.fixed, nil
	}

	cfg := d.cfg
	if cfg == nil {
		cfg = &decoration.Config{}
	}

	testCase, ok := cfg.Tier(tier.Name)
	if !ok {
		return Deps{}, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("decoration: no config for tier %q", tier.Name),
		)
	}

	err := tier.check(name, new(testCase.Overriding(name)))
	if err != nil {
		return Deps{}, err
	}

	return Build(name, new(testCase.For(name)), d.collaborators())
}

// Inventory returns every component and operation d resolved, ordered by tier then name:
// what a deployable prints with --print-decorations. The zero and a Fixed Decorations
// report no layers.
func (d Decorations) Inventory() []Resolved {
	comps := d.Components()
	out := make([]Resolved, 0, len(comps))

	for _, c := range comps {
		resolved := Resolved{Tier: c.Tier, Name: c.Name}
		if d.cfg != nil && d.fixed == nil {
			if tc, ok := d.cfg.Tier(c.Tier); ok {
				resolved.Layers, resolved.Sources = tc.For(c.Name), tc.Sources(c.Name)
			}
		}

		out = append(out, resolved)
	}

	return out
}

// Subject returns the Subject of the component named name in tier, decorated as Deps
// resolves it. A component built where no error can be returned (a pipeline stage, a
// job, a gate) resolves through it: a resolution error is recorded for Verify, and the
// Subject runs with recovery only until boot fails on it.
func (d Decorations) Subject(tier Tier, name string) Subject {
	deps, err := d.Deps(tier, name)
	if err != nil {
		d.reg.fail(err)

		return tier.Subject(name, &Deps{})
	}

	return tier.Subject(name, &deps)
}

// Resolved is one component or operation Decorations resolved, with the layers it runs
// and the config keys they came from.
type Resolved struct {
	// Tier is the tier's name ("service", "repository", …).
	Tier string

	// Name is the resolved dotted name: a component ("aiops.budgets") or one of its
	// operations ("aiops.budgets.create").
	Name string

	// Sources are the config keys merged into Layers, in order: "defaults", then each
	// override from the shortest dotted prefix to the name's own.
	Sources []string

	// Layers are the layers the name resolved to.
	Layers decoration.Layers
}

// collaborators returns the collaborators d was built over: none on the zero Decorations.
func (d Decorations) collaborators() Collaborators {
	if d.collab == nil {
		return Collaborators{}
	}

	return *d.collab
}

// ownOverride returns the override configured for exactly name in tier: none on the zero
// or a Fixed Decorations.
func (d Decorations) ownOverride(tier Tier, name string) decoration.Layers {
	if d.cfg == nil || d.fixed != nil {
		return decoration.Layers{}
	}

	tc, ok := d.cfg.Tier(tier.Name)
	if !ok {
		return decoration.Layers{}
	}

	return tc.Overrides[name]
}

// Build returns the Deps of the component named name from its resolved layers: each
// layer that is on gets its collaborator from c, or its primitive built from its tuning
// (the breaker is named name). An invalid tuning is CodeInvalidInput.
// l must not be nil.
func Build(name string, lRef *decoration.Layers, c Collaborators) (Deps, error) {
	layers := *lRef

	deps := observabilityDeps(&layers, c)

	err := addCallGuards(&deps, name, &layers)
	if err != nil {
		return Deps{}, err
	}

	err = addLoadGuards(&deps, name, &layers)
	if err != nil {
		return Deps{}, err
	}

	return deps, nil
}

// observabilityDeps selects the collaborators of the enabled observability layers.
func observabilityDeps(layers *decoration.Layers, collaborators Collaborators) Deps {
	deps := Deps{SkipRecovery: !decoration.On(layers.Recovery)}
	if !deps.SkipRecovery {
		deps.PanicLogger = collaborators.Logger
	}

	if decoration.On(layers.Logging) {
		deps.Logger = collaborators.Logger
	}

	if decoration.On(layers.Metrics) {
		deps.Metrics = collaborators.Metrics
	}

	if decoration.On(layers.Tracing) {
		deps.Tracer = collaborators.Tracer
	}

	if decoration.On(layers.Auth) {
		deps.Auth = collaborators.Auth
	}

	if layers.Timeout != nil {
		deps.Timeout = *layers.Timeout
	}

	return deps
}

// addCallGuards builds the retrier and circuit breaker when enabled.
func addCallGuards(deps *Deps, name string, layers *decoration.Layers) error {
	var err error

	if decoration.On(layers.Retry.Enabled) {
		deps.Retrier, err = newRetrier(layers.Retry)
		if err != nil {
			return wrapLayer(err, name, "retry")
		}
	}

	if decoration.On(layers.CircuitBreaker.Enabled) {
		deps.Breaker, err = newBreaker(name, layers.CircuitBreaker)
		if err != nil {
			return wrapLayer(err, name, "circuit_breaker")
		}
	}

	return nil
}

// addLoadGuards builds the bulkhead and rate limiter when enabled.
func addLoadGuards(deps *Deps, name string, layers *decoration.Layers) error {
	var err error

	if decoration.On(layers.Bulkhead.Enabled) {
		deps.Bulkhead, err = bulkheadFromConfig(layers.Bulkhead)
		if err != nil {
			return wrapLayer(err, name, "bulkhead")
		}

		deps.ShedLoad = decoration.On(layers.Bulkhead.Shed)
	}

	if decoration.On(layers.RateLimit.Enabled) {
		deps.RateLimiter, err = newRateLimiter(layers.RateLimit)
		if err != nil {
			return wrapLayer(err, name, "rate_limit")
		}
	}

	return nil
}

// newRetrier builds the exponential-backoff retrier r tunes.
func newRetrier(retryValue decoration.Retry) (interfaces.Retrier, error) {
	err := retryValue.RetryConfig.Validate()
	if err != nil {
		return nil, err
	}

	return retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      retryValue.RetryConfig.MaxRetries,
		InitialInterval: retryValue.RetryConfig.InitialInterval,
		MaxInterval:     retryValue.RetryConfig.MaxInterval,
		Multiplier:      retryValue.RetryConfig.Multiplier,
		MaxElapsedTime:  retryValue.RetryConfig.MaxElapsedTime,
	})
}

// newBreaker builds the breaker named name that b tunes.
func newBreaker(
	name string,
	circuitBreaker decoration.CircuitBreaker,
) (interfaces.CircuitBreaker, error) {
	err := circuitBreaker.BreakerConfig.Validate()
	if err != nil {
		return nil, err
	}

	return circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                name,
		MaxRequests:         circuitBreaker.BreakerConfig.MaxRequests,
		Interval:            circuitBreaker.BreakerConfig.Interval,
		Timeout:             circuitBreaker.BreakerConfig.Timeout,
		ConsecutiveFailures: circuitBreaker.BreakerConfig.ConsecutiveFailures,
		FailureRatio:        circuitBreaker.BreakerConfig.FailureRatio,
		MinRequests:         circuitBreaker.BreakerConfig.MinRequests,
	})
}

// bulkheadFromConfig builds the bulkhead b tunes; its kind is "channel" (or empty) or
// "adaptive".
func bulkheadFromConfig(bulkheadValue decoration.Bulkhead) (interfaces.Bulkhead, error) {
	err := bulkheadValue.BulkheadConfig.Validate()
	if err != nil {
		return nil, err
	}

	var kind bulkhead.Kind

	switch bulkheadValue.BulkheadConfig.Kind {
	case "", "channel":
		kind = bulkhead.KindChannel
	case "adaptive":
		kind = bulkhead.KindAdaptive
	default:
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"unknown bulkhead kind %q (want channel|adaptive)",
			bulkheadValue.BulkheadConfig.Kind,
		))
	}

	return bulkhead.NewFromConfig(bulkhead.Config{
		Kind:              kind,
		MaxConcurrent:     bulkheadValue.BulkheadConfig.MaxConcurrent,
		MinConcurrent:     bulkheadValue.BulkheadConfig.MinConcurrent,
		InitialConcurrent: bulkheadValue.BulkheadConfig.InitialConcurrent,
		RTTThreshold:      bulkheadValue.BulkheadConfig.RTTThreshold,
		BackoffRatio:      bulkheadValue.BulkheadConfig.BackoffRatio,
	})
}

// newRateLimiter builds the token-bucket limiter r tunes.
func newRateLimiter(rateLimit decoration.RateLimit) (interfaces.RateLimiter, error) {
	err := rateLimit.RateLimitConfig.Validate()
	if err != nil {
		return nil, err
	}

	return ratelimiter.New(
		ratelimiter.KindToken,
		ratelimiter.WithRate(rateLimit.RateLimitConfig.Rate),
		ratelimiter.WithBurst(rateLimit.RateLimitConfig.Burst),
	)
}

// wrapLayer codes a layer build failure for the component named name.
func wrapLayer(err error, name, layer string) error {
	return apperr.Wrap(err, apperr.CodeInvalidInput, "decoration "+name+": "+layer)
}
