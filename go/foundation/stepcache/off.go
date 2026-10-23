package stepcache

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// off is the cache that never hits.
type off struct{}

// Hit reports a miss.
func (off) Hit(context.Context, string) (bool, error) { return false, nil }

// Record does nothing.
func (off) Record(context.Context, string) error { return nil }

// IsOff reports whether c is the off cache (or nil), so a caller can skip computing
// keys nothing will ever look up.
func IsOff(c interfaces.StepCache) bool {
	if c == nil {
		return true
	}
	_, ok := c.(off)
	return ok
}
