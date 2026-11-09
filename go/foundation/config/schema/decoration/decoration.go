// Package decoration is the config surface for every decorated component —
// controllers, clients, workflows, pipelines, services, repositories, stores, jobs and
// gates. Each tier has default Layers and per-component overrides keyed by the
// component's dotted name; an override applies to its name and every name beneath it
// ("api" covers "api.documents.create"), the deepest winning. Every layer (recovery,
// tracing, metrics, logging, auth, timeout, retry, circuit breaker, bulkhead, rate
// limit) is switched and tuned here, while its order stays the tier's, in code.
// decorate.Decorations turns a component's resolved Layers into its decorate.Deps.
//
// The default observes each request at its edges — its controller and workflow, and
// every client, job and gate it calls — and leaves the pipeline, service, repository
// and store tiers unobserved until switched on; resilience runs where today's
// components run it.
package decoration

import (
	"reflect"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// defaultLayerTimeout is the timeout layer's default deadline.
const defaultLayerTimeout = 30 * time.Second

// SectionKey is the config key the decoration section lives under.
const SectionKey = "decoration"

// Layers switches and tunes each decoration layer of one component. A nil switch (or
// a nil Timeout) inherits; Merge lays one Layers over another. A layer that is on
// still needs its collaborator (a tracer for tracing, an authorizer for auth), and
// runs only where the tier orders it.
type Layers struct {
	// Recovery turns a panic into an INTERNAL error.
	Recovery *bool `mapstructure:"recovery"`

	// Tracing opens a span per operation.
	Tracing *bool `mapstructure:"tracing"`

	// Metrics records the tier's operation, error and duration series.
	Metrics *bool `mapstructure:"metrics"`

	// Logging logs each operation.
	Logging *bool `mapstructure:"logging"`

	// Auth authorizes each operation as "<name>.<op>".
	Auth *bool `mapstructure:"auth"`

	// Timeout bounds each operation (each attempt, under retry); zero turns it off.
	Timeout *time.Duration `mapstructure:"timeout"`

	// Retry retries transient failures with exponential backoff.
	Retry Retry `mapstructure:"retry"`

	// CircuitBreaker sheds calls while the component keeps failing.
	CircuitBreaker CircuitBreaker `mapstructure:"circuit_breaker"`

	// RateLimit rejects operations over a token-bucket rate.
	RateLimit RateLimit `mapstructure:"rate_limit"`

	// Bulkhead bounds the operations in flight.
	Bulkhead Bulkhead `mapstructure:"bulkhead"`
}

// Retry switches and tunes the retry layer.
type Retry struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// RetryConfig tunes the backoff.
	RetryConfig resilience.RetryConfig `mapstructure:"retry_config,squash"`
}

// CircuitBreaker switches and tunes the circuit-breaker layer; the breaker is named
// after the component.
type CircuitBreaker struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// BreakerConfig tunes when the breaker trips and recovers.
	BreakerConfig resilience.BreakerConfig `mapstructure:"breaker_config,squash"`
}

// Bulkhead switches and tunes the bulkhead layer.
type Bulkhead struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// Shed rejects an operation when the bulkhead is full (CodeResourceExhausted)
	// instead of waiting for a slot.
	Shed *bool `mapstructure:"shed"`

	// BulkheadConfig tunes the concurrency limit ("channel" or "adaptive").
	BulkheadConfig resilience.BulkheadConfig `mapstructure:"bulkhead_config,squash"`
}

// RateLimit switches and tunes the rate-limit layer.
type RateLimit struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// RateLimitConfig tunes the token bucket.
	RateLimitConfig resilience.RateLimitConfig `mapstructure:"rate_limit_config,squash"`
}

// On reports whether a layer switch is on (nil is off).
func On(b *bool) bool { return b != nil && *b }

// Merge returns base with every value over sets laid on top: a non-nil switch or
// timeout and a non-zero tuning value win; anything else keeps base.
// base must not be nil.
// over must not be nil.
func Merge(base, over *Layers) Layers {
	merged := *base
	mergeValue(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(*over))

	return merged
}

// mergeValue lays src's set fields over dst, recursing into structs.
func mergeValue(dst, src reflect.Value) {
	for i := range src.NumField() {
		mergeField(dst.Field(i), src.Field(i))
	}
}

// mergeField recurses into a struct and copies any other value that is set.
func mergeField(value, item reflect.Value) {
	if item.Kind() == reflect.Struct {
		mergeValue(value, item)

		return
	}

	if !item.IsZero() {
		value.Set(item)
	}
}

// dottedPrefixes lists name's dotted prefixes, shortest first, ending with name itself.
func dottedPrefixes(name string) []string {
	var out []string

	for i := range len(name) {
		if name[i] == '.' {
			out = append(out, name[:i])
		}
	}

	return append(out, name)
}

// DefaultConfig returns today's decoration: recovery and auth wherever a tier orders
// them; every controller, client, workflow, job and gate observed, and the pipeline,
// service, repository and store tiers not; retry and a circuit breaker at the
// repository tier; and a 30 s timeout, a bulkhead and a breaker around the storage,
// outbox and Prometheus clients. Every other resilience layer, the controllers' rate
// limit and bulkhead included, is off; a controller's bulkhead, once enabled, sheds.
func DefaultConfig() Config {
	repos := base(false)

	repos.Retry.Enabled = new(true)
	repos.CircuitBreaker.Enabled = new(true)
	// A controller's bulkhead, once enabled, sheds: a request queued behind a full
	// bulkhead holds its connection, so the edge rejects it instead.
	controllers := base(true)

	controllers.Bulkhead.Shed = new(true)

	guarded := func() Layers {
		return Layers{
			Timeout: new(defaultLayerTimeout),
			Bulkhead: Bulkhead{
				Enabled:        new(true),
				Shed:           nil,
				BulkheadConfig: stdx.Zero[resilience.BulkheadConfig](),
			},
			CircuitBreaker: CircuitBreaker{
				Enabled:       new(true),
				BreakerConfig: stdx.Zero[resilience.BreakerConfig](),
			},
			Recovery:  nil,
			Tracing:   nil,
			Metrics:   nil,
			Logging:   nil,
			Auth:      nil,
			Retry:     stdx.Zero[Retry](),
			RateLimit: stdx.Zero[RateLimit](),
		}
	}

	return Config{
		Clients: Tier{Defaults: base(true), Overrides: map[string]Layers{
			"storage":    guarded(),
			"outbox.sqs": guarded(),
			"outbox.s3":  guarded(),
			"prometheus": guarded(),
		}},
		Controllers:  Tier{Defaults: controllers, Overrides: nil},
		Workflows:    Tier{Defaults: base(true), Overrides: nil},
		Pipelines:    Tier{Defaults: base(false), Overrides: nil},
		Services:     Tier{Defaults: base(false), Overrides: nil},
		Repositories: Tier{Defaults: repos, Overrides: nil},
		Stores:       Tier{Defaults: base(false), Overrides: nil},
		Jobs:         Tier{Defaults: base(true), Overrides: nil},
		Gates:        Tier{Defaults: base(true), Overrides: nil},
	}
}

// base is a tier's default layers: recovery and auth on, observability as observe
// says, every resilience layer off with its default tuning.
func base(observe bool) Layers {
	return Layers{
		Recovery: new(true),
		Tracing:  new(observe),
		Metrics:  new(observe),
		Logging:  new(observe),
		Auth:     new(true),
		Timeout:  new(time.Duration(0)),
		Retry: Retry{
			Enabled:     new(false),
			RetryConfig: resilience.DefaultRetryConfig(),
		},
		CircuitBreaker: CircuitBreaker{
			Enabled:       new(false),
			BreakerConfig: resilience.DefaultBreakerConfig(),
		},
		Bulkhead: Bulkhead{
			Enabled:        new(false),
			Shed:           new(false),
			BulkheadConfig: resilience.DefaultBulkheadConfig(),
		},
		RateLimit: RateLimit{
			Enabled:         new(false),
			RateLimitConfig: resilience.DefaultRateLimitConfig(),
		},
	}
}

// Load reads the decoration section from loader and lays it over DefaultConfig(): an
// absent section keeps the default, a partial one changes only what it sets, and a
// section that fails to decode is returned as CodeInvalidInput.
func Load(loader interfaces.ConfigLoader) (Config, error) {
	section := loader.Get(SectionKey)
	if section == nil {
		return DefaultConfig(), nil
	}

	var over Config

	err := loader.UnmarshalKey(SectionKey, &over)
	if err != nil {
		return Config{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"load decoration config",
		)
	}

	err = dottedOverrides(loader, section, &over)
	if err != nil {
		return Config{}, err
	}

	def := DefaultConfig()

	return def.Overlay(&over), nil
}
