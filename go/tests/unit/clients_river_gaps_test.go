package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	riverpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
)

// TestRiverRuntime_UninitializedHealth tests that both health probes fail on a
// Runtime that holds no connection pool.
//
// Why this test is important:
//   - Liveness/Readiness back the worker's health endpoints; a Runtime whose pool was
//     never constructed must report unhealthy on both probes rather than pass a
//     probe against a nil pool and let Kubernetes route traffic to a dead replica.
//
// What it tests:
//   - On a zero-value Runtime (nil pool), Liveness and Readiness both return an error.
func TestRiverRuntime_UninitializedHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt := &riverpkg.Runtime{} // no pool constructed

	require.Error(
		t,
		rt.Liveness(ctx),
		"liveness must fail when the pool is not constructed",
	)
	require.Error(
		t,
		rt.Readiness(ctx),
		"readiness must fail when the pool is not constructed",
	)
}
