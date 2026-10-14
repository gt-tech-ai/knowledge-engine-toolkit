// Package stub is the zero-infrastructure ByteCache: every Get misses and every write
// is dropped, so a graph boots and runs uncached without Redis.
package stub

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Cache is the always-miss ByteCache.
type Cache struct{}

var _ interfaces.ByteCache = Cache{}

// New returns the stub cache.
func New() Cache { return Cache{} }

// Get always misses.
func (Cache) Get(context.Context, string) ([]byte, bool) { return nil, false }

// Set drops the value.
func (Cache) Set(context.Context, string, []byte, time.Duration) {}

// Delete is a no-op.
func (Cache) Delete(context.Context, string) {}
