package decorators

import (
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// Config tunes the client resilience stack for one client tier. It is the minimal
// per-client surface (expands it): Enabled gates the whole stack, and
// the primitive sub-configs tune each layer. A Config with Enabled=false yields a
// bare (passthrough) stack — the backwards-compatible path.
type Config struct {
	// CircuitBreaker tunes the per-attempt breaker; its Name defaults to the client name.
	CircuitBreaker circuitbreaker.Config `yaml:"circuit_breaker" mapstructure:"circuit_breaker"`

	// Retry tunes the Retry layer; only consumed when RetryEnabled is true.
	Retry retry.Config `yaml:"retry" mapstructure:"retry"`

	// Bulkhead tunes the concurrency limit (the outermost layer).
	Bulkhead bulkhead.Config `yaml:"bulkhead" mapstructure:"bulkhead"`

	// Timeout is the per-attempt deadline; zero leaves the timeout layer off.
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`

	// Enabled gates the whole stack; false yields a bare passthrough (backwards-compatible).
	Enabled bool `yaml:"enabled" mapstructure:"enabled"`

	// RetryEnabled opts the client into the Retry layer (off by default so it never
	// doubles up with an SDK/existing retry authority).
	RetryEnabled bool `yaml:"retry_enabled" mapstructure:"retry_enabled"`
}

// DefaultConfig returns sensible production defaults: the stack enabled with
// Bulkhead/CircuitBreaker/Timeout on, and the Retry layer off (opt-in per client
// so it never doubles up with an SDK/existing retry authority).
func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		Timeout:      30 * time.Second,
		RetryEnabled: false,
		Bulkhead:     bulkhead.DefaultConfig(),
		Retry:        retry.DefaultConfig(),
		CircuitBreaker: circuitbreaker.DefaultConfig(
			"",
		), // name set per-client in StackFromConfig
	}
}

// Deps carries the injected observability collaborators. Any may be nil, which
// leaves that layer off.
type Deps struct {
	// Metrics records per-operation counts, errors, and latency.
	Metrics interfaces.Metrics

	// Logger records failed operations.
	Logger interfaces.Logger

	// Tracer emits a span per operation.
	Tracer oteltrace.Tracer
}

// StackFromConfig builds a [Stack] for the client named name from cfg and deps.
// A disabled config returns a bare passthrough stack (no layers). It fails loudly
// if a primitive rejects its config.
func StackFromConfig(name string, cfg Config, deps Deps) (*Stack, error) {
	s := New(name)
	if !cfg.Enabled {
		return s, nil
	}

	bh, err := bulkhead.NewFromConfig(cfg.Bulkhead)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"client stack: bulkhead",
		)
	}
	cbCfg := cfg.CircuitBreaker
	if cbCfg.Name == "" {
		cbCfg.Name = name
	}
	cb, err := circuitbreaker.NewFromConfig(cbCfg)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"client stack: circuit breaker",
		)
	}

	s.WithBulkhead(bh).
		WithCircuitBreaker(cb).
		WithTimeout(cfg.Timeout).
		WithMetrics(deps.Metrics).
		WithLogger(deps.Logger).
		WithTracer(deps.Tracer)

	if cfg.RetryEnabled {
		r, err := retry.NewFromConfig(cfg.Retry)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				"client stack: retry",
			)
		}
		s.WithRetrier(r)
	}
	return s, nil
}
