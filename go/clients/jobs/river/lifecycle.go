package river

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// The Runtime (which owns the pool + worker loop) is a full health-checkable client.
var _ interfaces.Client = (*Runtime)(nil)

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
