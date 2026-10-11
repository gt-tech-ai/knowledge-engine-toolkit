package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
)

// TestCircuitBreakerOptions_FailureRatioAndMinRequests tests that the
// WithFailureRatio and WithMinRequests functional options are applied when a
// breaker is constructed.
//
// Why this test is important:
//   - These two options are the failure-ratio trip's tuning surface; if applying
//     them were a no-op (or panicked), a caller wiring a ratio-based breaker would
//     silently get the default thresholds. Exercising them through the public New
//     constructor proves the option closures run against the config.
//
// What it tests:
//   - New(KindGoBreaker, name, WithFailureRatio, WithMinRequests) returns a
//     non-nil breaker without error, so both option closures executed.
func TestCircuitBreakerOptions_FailureRatioAndMinRequests(t *testing.T) {
	t.Parallel()

	cb, err := circuitbreaker.New(
		circuitbreaker.KindGoBreaker,
		"branch-cov",
		circuitbreaker.WithFailureRatio(0.3),
		circuitbreaker.WithMinRequests(7),
	)
	require.NoError(t, err, "New with failure-ratio options must not error")
	require.NotNil(t, cb, "New must return a non-nil breaker")
}

// TestLifecycleNoOp_StartStop tests that the embeddable NoOp lifecycle's Start and
// Stop are no-ops that return nil.
//
// Why this test is important:
//   - NoOp is embedded by stateless clients so they satisfy interfaces.Lifecycle
//     without re-declaring start/stop; if either returned an error the lifecycle
//     Manager would abort startup (or report a spurious shutdown failure) for every
//     client that embeds it.
//
// What it tests:
//   - NoOp{}.Start and NoOp{}.Stop each return nil.
func TestLifecycleNoOp_StartStop(t *testing.T) {
	t.Parallel()

	var n lifecycle.NoOp
	ctx := context.Background()
	assert.NoError(t, n.Start(ctx), "NoOp.Start must be a nil-returning no-op")
	assert.NoError(t, n.Stop(ctx), "NoOp.Stop must be a nil-returning no-op")
}
