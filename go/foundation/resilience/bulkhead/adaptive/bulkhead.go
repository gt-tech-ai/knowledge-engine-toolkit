// Package adaptive provides a self-tuning (AIMD) concurrency-limiting Bulkhead.
//
// Unlike the fixed-size channel bulkhead, the limit adjusts to observed latency: a sample
// slower than the RTT threshold — or a failed op, a load-shed signal — multiplicatively
// decreases the limit; a fast, successful sample additively increases it up to
// MaxConcurrent. This finds the concurrency that maximizes throughput without building a
// queue at a slowing backend..
package adaptive

import (
	"context"
	"math"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ErrBulkheadFull is returned by TryExecute when the current adaptive limit is reached.
var ErrBulkheadFull = apperr.New(
	apperr.CodeUnavailable,
	"bulkhead: adaptive concurrency limit reached",
)

// Compile-time interface assertion.
var _ interfaces.Bulkhead = (*Bulkhead)(nil)

// Config holds adaptive bulkhead settings.
type Config struct {
	// MinConcurrent is the floor the limit never drops below (coerced to >= 1).
	MinConcurrent int

	// MaxConcurrent is the ceiling the limit never grows above.
	MaxConcurrent int

	// InitialConcurrent is the starting limit (clamped into [Min, Max]).
	InitialConcurrent int

	// RTTThreshold marks the slow tail: a sample slower than this shrinks the limit. Zero
	// disables the latency signal (only a failed op then shrinks the limit).
	RTTThreshold time.Duration

	// BackoffRatio in (0,1) is the multiplicative-decrease factor on a slow/failed sample;
	// out-of-range values fall back to 0.9.
	BackoffRatio float64
}

// DefaultConfig returns sensible adaptive defaults.
func DefaultConfig() Config {
	return Config{
		MinConcurrent:     1,
		MaxConcurrent:     20,
		InitialConcurrent: 10,
		RTTThreshold:      100 * time.Millisecond,
		BackoffRatio:      0.9,
	}
}

// Bulkhead limits concurrency with a self-tuning (AIMD) limit.
type Bulkhead struct {
	// notify is a broadcast channel closed on each release to wake acquire waiters.
	notify chan struct{}
	// limit is the current adaptive concurrency limit (float for AIMD adjustment).
	limit float64
	// inFlight is the number of currently executing operations.
	inFlight int
	// minLimit is the lower clamp bound for limit.
	minLimit float64
	// maxLimit is the upper clamp bound for limit.
	maxLimit float64
	// rttThreshold is the latency above which a sample shrinks the limit (0 disables the
	// latency signal).
	rttThreshold time.Duration
	// backoffRatio is the multiplicative-decrease factor applied on a slow/failed sample.
	backoffRatio float64
	// mu guards limit, inFlight, and notify.
	mu sync.Mutex
}

// New creates an adaptive bulkhead from cfg, clamping the initial limit into [min, max].
func New(cfg Config) *Bulkhead {
	minL := math.Max(1, float64(cfg.MinConcurrent))
	maxL := math.Max(minL, float64(cfg.MaxConcurrent))
	init := math.Min(maxL, math.Max(minL, float64(cfg.InitialConcurrent)))
	ratio := cfg.BackoffRatio
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.9
	}
	return &Bulkhead{
		limit:        init,
		minLimit:     minL,
		maxLimit:     maxL,
		rttThreshold: cfg.RTTThreshold,
		backoffRatio: ratio,
		notify:       make(chan struct{}),
	}
}

// Execute runs fn within the current adaptive limit, blocking until a slot frees or ctx
// is cancelled, then adjusts the limit from fn's latency and outcome.
func (b *Bulkhead) Execute(ctx context.Context, fn func() error) error {
	if err := b.acquire(ctx); err != nil {
		return err
	}
	start := time.Now()
	err := fn()
	b.release(time.Since(start), err)
	return err
}

// TryExecute runs fn only if a slot is immediately available under the current limit,
// otherwise returns ErrBulkheadFull; it adjusts the limit from fn's latency and outcome.
func (b *Bulkhead) TryExecute(fn func() error) error {
	b.mu.Lock()
	if b.inFlight >= int(b.limit) {
		b.mu.Unlock()
		return ErrBulkheadFull
	}
	b.inFlight++
	b.mu.Unlock()

	start := time.Now()
	err := fn()
	b.release(time.Since(start), err)
	return err
}

// Limit reports the current adaptive concurrency limit (for metrics / observability).
func (b *Bulkhead) Limit() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.limit)
}

// acquire blocks until in-flight is below the current limit, or ctx is cancelled.
func (b *Bulkhead) acquire(ctx context.Context) error {
	for {
		b.mu.Lock()
		if b.inFlight < int(b.limit) {
			b.inFlight++
			b.mu.Unlock()
			return nil
		}
		wait := b.notify
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return apperr.Wrap(
				ctx.Err(),
				apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
				"bulkhead wait canceled",
			)
		case <-wait:
			// A slot was released; re-check the (possibly changed) limit.
		}
	}
}

// release decrements in-flight, adjusts the limit from the sample, and broadcasts a freed
// slot.
func (b *Bulkhead) release(rtt time.Duration, opErr error) {
	b.mu.Lock()
	if b.inFlight > 0 {
		b.inFlight--
	}
	b.adjust(rtt, opErr)
	close(b.notify)
	b.notify = make(chan struct{})
	b.mu.Unlock()
}

// adjust applies AIMD (caller holds b.mu): a slow or failed sample multiplicatively
// decreases the limit; a fast, successful sample additively increases it, clamped into
// [min, max].
func (b *Bulkhead) adjust(rtt time.Duration, opErr error) {
	if opErr != nil || (b.rttThreshold > 0 && rtt > b.rttThreshold) {
		b.limit = math.Max(b.minLimit, b.limit*b.backoffRatio)
		return
	}
	b.limit = math.Min(b.maxLimit, b.limit+1)
}
