// Package pausegate stops a consumer pulling work while its downstream is unavailable.
// The consumer reports each unit of work's outcome to Observe; an outage-coded error
// closes the gate for a pause, and the consumer's receive loop checks Ready before every
// receive, so no message's delivery count climbs toward a dead-letter queue during an
// outage it cannot fix.
package pausegate

import (
	"context"
	"slices"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Config tunes a Gate.
type Config struct {
	Codes []apperr.ErrorCode `yaml:"codes" mapstructure:"codes"`
	Pause time.Duration      `yaml:"pause" mapstructure:"pause"`
}

// Gate is open until Observe sees an outage-coded error, then closed for Config.Pause.
type Gate struct {
	// until is when the gate reopens; the zero time means open.
	until time.Time
	// paused reports 1 while the gate is closed and 0 once it reopens; nil skips it.
	paused interfaces.Gauge
	// now reads the clock.
	now func() time.Time
	// codes close the gate.
	codes []apperr.ErrorCode
	// pause is Config.Pause.
	pause time.Duration
	// mu guards until.
	mu sync.Mutex
}

// New returns an open Gate for cfg that reports its state on paused (nil skips the
// gauge) and reads time from now. A non-positive Pause is CodeInvalidInput; a nil now
// panics, as a wiring bug.
func New(cfg Config, paused interfaces.Gauge, now func() time.Time) (*Gate, error) {
	if now == nil {
		panic("pausegate: now must not be nil")
	}
	if cfg.Pause <= 0 {
		return nil, apperr.InvalidInput("pausegate: pause must be positive")
	}
	codes := cfg.Codes
	if len(codes) == 0 {
		codes = []apperr.ErrorCode{apperr.CodeUnavailable}
	}
	return &Gate{pause: cfg.Pause, codes: codes, paused: paused, now: now}, nil
}

// Observe closes the gate for the pause when err carries one of the gate's codes; any
// other result, nil included, leaves it as it is.
func (g *Gate) Observe(err error) {
	if err == nil || !slices.Contains(g.codes, apperr.Code(err)) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.until = g.now().Add(g.pause)
	g.set(1)
}

// Ready returns a CodeUnavailable error while the gate is closed and nil once it is
// open; its signature is a subscriber's receive gate.
func (g *Gate) Ready(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now().Before(g.until) {
		return apperr.New(
			apperr.CodeUnavailable,
			"pausegate: downstream unavailable, paused",
		)
	}
	g.set(0)
	return nil
}

// set reports v on the paused gauge when there is one.
func (g *Gate) set(v float64) {
	if g.paused != nil {
		g.paused.Set(v)
	}
}
