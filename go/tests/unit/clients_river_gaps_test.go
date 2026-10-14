package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	riverpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
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

// TestNewInsertClient_FailsLoudlyBeforeOpeningAPool tests the insert client's
// composition-time checks.
//
// Why this test is important:
//   - A producer misconfigured by its decoration or its DSN must stop at boot rather
//     than silently drop the jobs it enqueues.
//
// What it tests:
//   - An invalid tuning of the "river.insert" client is INVALID_INPUT; a malformed DSN
//     under a valid decoration is INTERNAL. Neither opens a connection.
func TestNewInsertClient_FailsLoudlyBeforeOpeningAPool(t *testing.T) {
	t.Parallel()
	on := true
	bad := decoration.DefaultConfig()
	semaphore := decoration.Bulkhead{Enabled: &on}
	semaphore.Kind = "semaphore"
	bad.Clients.Overrides = map[string]decoration.Layers{
		riverpkg.InsertClientName: {Bulkhead: semaphore},
	}

	_, err := riverpkg.NewInsertClient(t.Context(), "postgres://unused", 1,
		decorate.NewDecorations(&bad, decorate.Collaborators{}))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	defaults := decoration.DefaultConfig()
	_, err = riverpkg.NewInsertClient(t.Context(), "::not a dsn::", 1,
		decorate.NewDecorations(&defaults, decorate.Collaborators{}))
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}
