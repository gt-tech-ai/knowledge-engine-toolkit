// Package gobreaker provides a circuit breaker backed by sony/gobreaker/v2.
package gobreaker

import (
	"time"

	gb "github.com/sony/gobreaker/v2"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
)

// Compile-time interface assertion.
var _ interfaces.CircuitBreaker = (*Breaker)(nil)

// Config holds gobreaker-specific circuit breaker settings.
type Config struct {
	// Name identifies the circuit breaker instance for logging and metrics.
	Name string

	// Interval is the cyclic period of the closed state for clearing internal counts.
	Interval time.Duration

	// Timeout is the duration the circuit stays open before transitioning to half-open.
	Timeout time.Duration

	// MaxRequests is the number of requests allowed in the half-open state.
	MaxRequests uint32

	// ConsecutiveFailures is the threshold of consecutive failures that trips the breaker.
	ConsecutiveFailures uint32

	// FailureRatio trips the breaker when the failure fraction over the interval
	// reaches it (once MinRequests is met), independent of consecutive failures. It
	// catches a steady interleaving of successes and failures that never accumulates
	// ConsecutiveFailures but still signals an unhealthy dependency.
	FailureRatio float64

	// MinRequests is the minimum number of requests observed in the interval before
	// the FailureRatio guard applies, so the breaker never trips on a tiny sample.
	MinRequests uint32
}

// DefaultConfig returns sensible defaults for the given circuit breaker name.
func DefaultConfig(name string) Config {
	return Config{
		Name:                name,
		MaxRequests:         1,
		Interval:            60 * time.Second,
		Timeout:             30 * time.Second,
		ConsecutiveFailures: 5,
		FailureRatio:        0.5,
		MinRequests:         10,
	}
}

// Breaker implements interfaces.CircuitBreaker backed by sony/gobreaker.
type Breaker struct {
	// inner is the underlying gobreaker circuit breaker.
	inner *gb.CircuitBreaker[struct{}]
}

// New creates a circuit breaker from the given config.
func New(cfg Config) *Breaker {
	cb := gb.NewCircuitBreaker[struct{}](gb.Settings{
		Name:        cfg.Name,
		MaxRequests: cfg.MaxRequests,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		// Only transient infrastructure failures (the same class the retrier
		// retries) count against the breaker; permanent domain errors — NotFound,
		// Conflict, InvalidInput, and the auth codes — are treated as successful so
		// ordinary business outcomes never trip it. The shared retryability
		// policy lives in retry/exponential.
		IsSuccessful: func(err error) bool {
			return err == nil || !exponential.IsRetryable(err)
		},
		ReadyToTrip: func(counts gb.Counts) bool {
			// Consecutive failures remain a floor; additionally trip on a sustained
			// failure ratio once a minimum request volume is seen.
			if counts.ConsecutiveFailures >= cfg.ConsecutiveFailures {
				return true
			}
			return cfg.MinRequests > 0 &&
				counts.Requests >= cfg.MinRequests &&
				float64(counts.TotalFailures)/float64(counts.Requests) >= cfg.FailureRatio
		},
	})
	return &Breaker{inner: cb}
}

// Execute runs fn through the circuit breaker. A call the breaker rejects (open, or
// half-open with its probe quota spent) is CodeUnavailable; fn's own error is returned
// unchanged.
func (b *Breaker) Execute(fn func() error) error {
	_, err := b.inner.Execute(func() (struct{}, error) {
		return struct{}{}, fn()
	})
	if apperr.StdIs(err, gb.ErrOpenState) || apperr.StdIs(err, gb.ErrTooManyRequests) {
		return apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"circuit breaker "+b.inner.Name()+" open",
		)
	}
	return err
}
