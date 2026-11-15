// Package token provides a token-bucket rate limiter backed by golang.org/x/time/rate.
package token

import (
	"context"

	"golang.org/x/time/rate"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// defaultConfigRate is the default Rate of Config.
const defaultConfigRate = 100

// defaultConfigBurst is the default Burst of Config.
const defaultConfigBurst = 10

// Compile-time interface assertion.
var _ interfaces.RateLimiter = (*Limiter)(nil)

// Config holds token bucket rate limiter settings.
type Config struct {
	// Recorder counts each refused Allow as rate_limited with no operation; nil records
	// nothing. It is wired in code, never decoded from config.
	Recorder *series.Recorder `yaml:"-" mapstructure:"-" json:"-"`

	// Rate is the sustained events per second allowed.
	Rate float64

	// Burst is the maximum number of events allowed in a single burst.
	Burst int
}

// DefaultConfig returns sensible defaults for token bucket rate limiting.
func DefaultConfig() Config {
	return Config{
		Recorder: nil,
		Rate:     defaultConfigRate,
		Burst:    defaultConfigBurst,
	}
}

// Limiter implements interfaces.RateLimiter using a token bucket algorithm.
type Limiter struct {
	// inner is the underlying token bucket rate limiter.
	inner *rate.Limiter

	// recorder counts refused Allow calls; nil records nothing.
	recorder *series.Recorder
}

// New creates a token bucket rate limiter from the given config.
func New(cfg Config) *Limiter {
	return &Limiter{
		inner:    rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Burst),
		recorder: cfg.Recorder,
	}
}

// Allow reports whether an event may happen now (non-blocking); a refusal is counted
// through Config.Recorder.
func (l *Limiter) Allow() bool {
	if l.inner.Allow() {
		return true
	}

	l.recorder.Rejected("", series.ReasonRateLimited)

	return false
}

// Wait blocks until the limiter permits an event or ctx is cancelled.
func (l *Limiter) Wait(ctx context.Context) error {
	err := l.inner.Wait(ctx)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.ContextCode(err, apperr.CodeTimeout),
			"rate limiter wait",
		)
	}

	return nil
}
