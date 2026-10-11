// Package adaptivethrottle provides a client-side adaptive throttler (Google SRE
// "Handling Overload"): it sheds a growing fraction of outbound requests locally when a
// backend's accept-rate drops, protecting the backend from a client-side retry storm.
//
// Compose it OUTERMOST of the retry budget (Throttle → Retry → op): a local rejection
// returns ErrThrottled before op runs, so the inner retrier never retries a shed request
// — the "not retried" property the retry budget relies on..
package adaptivethrottle

import (
	"context"
	"math"
	"math/rand/v2"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ErrThrottled is returned when the throttler sheds a request locally without calling op.
var ErrThrottled = apperr.New(
	apperr.CodeUnavailable,
	"adaptivethrottle: request shed by client-side adaptive throttling",
)

// Compile-time interface assertion.
var _ interfaces.AdaptiveThrottler = (*Throttler)(nil)

// Config tunes the SRE throttler.
type Config struct {
	// Rand is the [0,1) random source used for probabilistic shedding (nil → rand.Float64).
	Rand func() float64
	// K is the accepts multiplier in the rejection formula; higher K sheds less
	// aggressively.
	K float64
	// Decay is the per-attempt exponential decay (0 < Decay ≤ 1) applied to the
	// request/accept windows.
	Decay float64
}

// DefaultConfig returns SRE-typical throttler defaults.
func DefaultConfig() Config {
	return Config{K: 2.0, Decay: 0.98}
}

// Throttler is the SRE client-side adaptive throttler.
type Throttler struct {
	// rand is the [0,1) random source used to decide whether to shed a request.
	rand func() float64
	// requests is the decayed count of recent request attempts (shed or allowed).
	requests float64
	// accepts is the decayed count of recent successful accepts.
	accepts float64
	// k is the accepts multiplier in the rejection formula.
	k float64
	// decay is the per-attempt window decay factor.
	decay float64
	// mu guards requests and accepts.
	mu sync.Mutex
}

// NewDisabled returns a no-op AdaptiveThrottler that never sheds: Do runs op directly and
// RejectionProbability is always 0. It is the "disabled" selection for the config-driven
// throttle tier (resilience.adaptive_throttle kind=disabled) — a behavior-preserving
// pass-through, mirroring hedge's disabled default.
func NewDisabled() interfaces.AdaptiveThrottler { return disabledThrottler{} }

// disabledThrottler is the no-op throttler: it never sheds a request.
type disabledThrottler struct{}

// compile-time check: disabledThrottler satisfies the AdaptiveThrottler contract.
var _ interfaces.AdaptiveThrottler = disabledThrottler{}

// Do runs op directly, without shedding.
func (disabledThrottler) Do(_ context.Context, op func() error) error { return op() }

// RejectionProbability always reports 0 — a disabled throttler never sheds.
func (disabledThrottler) RejectionProbability() float64 { return 0 }

// New creates a Throttler from cfg, coercing out-of-range K/Decay to their defaults.
func New(cfg Config) *Throttler {
	k := cfg.K
	if k <= 0 {
		k = 2.0
	}
	decay := cfg.Decay
	if decay <= 0 || decay > 1 {
		decay = 0.98
	}
	r := cfg.Rand
	if r == nil {
		r = rand.Float64
	}
	return &Throttler{k: k, decay: decay, rand: r}
}

// Do runs op unless the throttler sheds it locally, in which case it returns ErrThrottled
// WITHOUT invoking op. A success feeds the accept window; every attempt (shed or allowed)
// feeds the request window, so a sustained backend failure raises the rejection
// probability and a recovery lowers it.
func (t *Throttler) Do(_ context.Context, op func() error) error {
	t.mu.Lock()
	// Age the window, then decide from the pre-attempt ratio so a cold start (0/0) never
	// sheds.
	t.requests *= t.decay
	t.accepts *= t.decay
	shed := t.rand() < t.rejectionProbability()
	t.requests++ // this attempt counts as a request whether shed or allowed
	t.mu.Unlock()

	if shed {
		return ErrThrottled
	}
	err := op()
	if err == nil {
		t.mu.Lock()
		t.accepts++
		t.mu.Unlock()
	}
	return err
}

// RejectionProbability reports the current probability that a request is shed locally
// (for metrics / observability).
func (t *Throttler) RejectionProbability() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rejectionProbability()
}

// rejectionProbability is the SRE formula max(0, (requests − K·accepts) / (requests +
// 1)); the caller holds t.mu.
func (t *Throttler) rejectionProbability() float64 {
	return math.Max(0, (t.requests-t.k*t.accepts)/(t.requests+1))
}
