// Package token provides a token-bucket rate limiter backed by golang.org/x/time/rate.
package token

import (
	"context"

	"golang.org/x/time/rate"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultConfigRate is the default Rate of Config.
const defaultConfigRate = 100

// defaultConfigBurst is the default Burst of Config.
const defaultConfigBurst = 10

// Compile-time interface assertion.
var _ interfaces.RateLimiter = (*Limiter)(nil)

// Config holds token bucket rate limiter settings.
type Config struct {
	// Rate is the sustained events per second allowed.
	Rate float64

	// Burst is the maximum number of events allowed in a single burst.
	Burst int
}

// DefaultConfig returns sensible defaults for token bucket rate limiting.
func DefaultConfig() Config {
	return Config{
		Rate:  defaultConfigRate,
		Burst: defaultConfigBurst,
	}
}

// Limiter implements interfaces.RateLimiter using a token bucket algorithm.
type Limiter struct {
	// inner is the underlying token bucket rate limiter.
	inner *rate.Limiter
}

// New creates a token bucket rate limiter from the given config.
func New(cfg Config) *Limiter {
	return &Limiter{
		inner: rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Burst),
	}
}

// Allow reports whether an event may happen now (non-blocking).
func (l *Limiter) Allow() bool {
	return l.inner.Allow()
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
