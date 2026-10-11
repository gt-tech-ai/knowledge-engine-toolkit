package redis

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time check: the Redis cache is a lifecycle-managed, health-checkable
// client, so a lifecycle.Manager coordinates its startup/shutdown and Kubernetes
// probes its health.
var _ interfaces.Client = (*Cache)(nil)

// Start verifies the Redis connection is reachable. The connection pool is created
// in New; Start pings so an unreachable Redis fails fast at the composition root
// rather than on the first cache operation.
func (c *Cache) Start(ctx context.Context) error {
	return c.Ping(ctx)
}

// Stop closes the Redis connection pool.
func (c *Cache) Stop(_ context.Context) error {
	return c.Close()
}

// Liveness reports whether the cache holds a connection pool (a lightweight
// self-check that does not touch Redis).
func (c *Cache) Liveness(_ context.Context) error {
	if c.client == nil {
		return apperr.Internal("redis cache not initialized")
	}
	return nil
}

// Readiness verifies Redis can serve traffic (a Ping).
func (c *Cache) Readiness(ctx context.Context) error {
	return c.Ping(ctx)
}
