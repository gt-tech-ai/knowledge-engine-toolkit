package river

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time checks: the enqueuer client is lifecycle-managed, and the Runtime
// (which owns the pool + worker loop) is a full health-checkable client.
var (
	// Client (the enqueuer) is lifecycle-managed (Start/Stop only).
	_ interfaces.Lifecycle = (*Client)(nil)
	// Runtime (pool + worker loop) is a full health-checkable client.
	_ interfaces.Client = (*Runtime)(nil)
)

// Start is a no-op for the enqueuer: it holds no connection of its own (the pool
// belongs to the Runtime), so there is nothing to open. It exists so the enqueuer
// registers uniformly with a lifecycle.Manager.
func (c *Client) Start(_ context.Context) error { return nil }

// Stop releases the enqueuer's resources.
func (c *Client) Stop(_ context.Context) error { return c.Close() }

// Liveness reports whether the Runtime holds a connection pool.
func (r *Runtime) Liveness(_ context.Context) error {
	if r.pool == nil {
		return apperr.Internal("river runtime not initialized")
	}
	return nil
}

// Readiness verifies the Runtime's database pool is reachable (a Ping).
func (r *Runtime) Readiness(ctx context.Context) error {
	if r.pool == nil {
		return apperr.Internal("river runtime not initialized")
	}
	return apperr.Wrap(
		r.pool.Ping(ctx),
		apperr.CodeUnavailable,
		"river pool ping",
	)
}
