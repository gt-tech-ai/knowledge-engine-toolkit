// Package keyed is the in-process KeyedLimiter: a per-key counting semaphore that
// refuses, rather than waits, once a key holds its cap.
package keyed

import (
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time check: a Limiter is a KeyedLimiter.
var _ interfaces.KeyedLimiter = (*Limiter)(nil)

// Config sizes a Limiter.
type Config struct {
	// MaxPerKey is the most slots one key may hold at once; it must be positive.
	MaxPerKey int `yaml:"max_per_key" mapstructure:"max_per_key"`
}

// Validate rejects a non-positive MaxPerKey with CodeInvalidInput.
func (c Config) Validate() error {
	if c.MaxPerKey <= 0 {
		return apperr.InvalidInput("keyed limiter: max_per_key must be positive")
	}
	return nil
}

// Limiter counts each key's held slots; a key with none has no entry, so the map holds
// only keys with work in flight.
type Limiter struct {
	// held counts each key's slots in use.
	held map[string]int
	// limit is Config.MaxPerKey.
	limit int
	// mu guards held.
	mu sync.Mutex
}

// New returns a Limiter for cfg; an invalid cfg is CodeInvalidInput.
func New(cfg Config) (*Limiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Limiter{held: map[string]int{}, limit: cfg.MaxPerKey}, nil
}

// TryAcquire takes one of key's slots, or reports false when key holds its cap. The
// returned release is idempotent.
func (l *Limiter) TryAcquire(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] >= l.limit {
		return nil, false
	}
	l.held[key]++
	var once sync.Once
	return func() { once.Do(func() { l.release(key) }) }, true
}

// release frees one of key's slots, dropping the entry once the key holds none.
func (l *Limiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held[key]--
	if l.held[key] <= 0 {
		delete(l.held, key)
	}
}
