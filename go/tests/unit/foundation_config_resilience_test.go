package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/adaptivethrottle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/hedge"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter/token"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// TestResilienceDefaults_MatchPrimitives tests that each schema/resilience
// Default* returns values identical to the primitive's DefaultConfig().
//
// Why this test is important:
//   - The config surface duplicates the primitive's tunable fields; adopting it
//     is only behavior-preserving if the defaults match exactly. This test is the
//     drift guard — if a primitive default changes and the schema mirror does not
//     (or vice-versa), config-sourced providers would silently change behavior.
//
// What it tests:
//   - Retry / breaker / bulkhead / rate-limit schema defaults equal the
//     corresponding primitive DefaultConfig() field-by-field.
func TestResilienceDefaults_MatchPrimitives(t *testing.T) {
	t.Parallel()

	retryConfig := resilience.DefaultRetryConfig()
	pr := retry.DefaultConfig()
	assert.Equal(t, pr.MaxRetries, retryConfig.MaxRetries)
	assert.Equal(t, pr.InitialInterval, retryConfig.InitialInterval)
	assert.Equal(t, pr.MaxInterval, retryConfig.MaxInterval)
	assert.InDelta(t, pr.Multiplier, retryConfig.Multiplier, 0)
	assert.Equal(t, pr.MaxElapsedTime, retryConfig.MaxElapsedTime)

	breakerConfig := resilience.DefaultBreakerConfig()
	config := circuitbreaker.DefaultConfig(textX)
	assert.Equal(t, config.MaxRequests, breakerConfig.MaxRequests)
	assert.Equal(t, config.Interval, breakerConfig.Interval)
	assert.Equal(t, config.Timeout, breakerConfig.Timeout)
	assert.Equal(t, config.ConsecutiveFailures, breakerConfig.ConsecutiveFailures)
	assert.InDelta(t, config.FailureRatio, breakerConfig.FailureRatio, 0)
	assert.Equal(t, config.MinRequests, breakerConfig.MinRequests)

	bulkheadConfig := resilience.DefaultBulkheadConfig()
	pbh := bulkhead.DefaultConfig()
	assert.Equal(t, pbh.Kind.String(), bulkheadConfig.Kind,
		"schema bulkhead default kind must mirror the primitive default (channel)")
	assert.Equal(t, pbh.MaxConcurrent, bulkheadConfig.MaxConcurrent)
	assert.Equal(t, pbh.MinConcurrent, bulkheadConfig.MinConcurrent)
	assert.Equal(t, pbh.InitialConcurrent, bulkheadConfig.InitialConcurrent)
	assert.Equal(t, pbh.RTTThreshold, bulkheadConfig.RTTThreshold)
	assert.InDelta(t, pbh.BackoffRatio, bulkheadConfig.BackoffRatio, 0)

	rl := resilience.DefaultRateLimitConfig()
	prl := token.DefaultConfig()
	assert.InDelta(t, prl.Rate, rl.Rate, 0)
	assert.Equal(t, prl.Burst, rl.Burst)

	h := resilience.DefaultHedgeConfig()
	ph := hedge.DefaultConfig()
	assert.Equal(t, ph.Kind.String(), h.Kind,
		"schema hedge default kind must mirror the primitive default (disabled)")
	assert.Equal(t, ph.Delay, h.Delay)

	adaptiveThrottleConfig := resilience.DefaultAdaptiveThrottleConfig()
	pat := adaptivethrottle.DefaultConfig()
	assert.InDelta(
		t,
		pat.K,
		adaptiveThrottleConfig.K, 0,
		"schema adaptive_throttle K default must mirror the primitive",
	)
	assert.InDelta(
		t,
		pat.Decay,
		adaptiveThrottleConfig.Decay, 0,
		"schema adaptive_throttle Decay default must mirror the primitive",
	)
	assert.Equal(t, "disabled", adaptiveThrottleConfig.Kind,
		"adaptive_throttle defaults to disabled (opt-in load shedding)")
}

// TestResilienceValidate_RejectsBadValues tests that Validate() rejects the
// out-of-range values named in its contract.
//
// Why this test is important:
//   - The config surface is meant to be tuned per environment; Validate is the
//     guard that a fat-fingered overlay (negative interval, ratio > 1) fails loudly
//     at load rather than producing a silently-broken decorator.
//
// What it tests:
//   - A negative retry interval, a sub-1 multiplier, and an out-of-[0,1] breaker
//     failure ratio are each rejected; the defaults validate cleanly.
func TestResilienceValidate_RejectsBadValues(t *testing.T) {
	t.Parallel()

	require.NoError(t, resilience.DefaultRetryConfig().Validate())
	require.NoError(t, resilience.DefaultBreakerConfig().Validate())

	bad := resilience.DefaultRetryConfig()

	bad.InitialInterval = -1
	require.Error(t, bad.Validate(), "negative initial_interval must be rejected")

	badMul := resilience.DefaultRetryConfig()

	badMul.Multiplier = 0
	require.Error(t, badMul.Validate(), "multiplier < 1 must be rejected")

	badRatio := resilience.DefaultBreakerConfig()

	badRatio.FailureRatio = failureRatio2
	assert.Error(t, badRatio.Validate(), "failure_ratio > 1 must be rejected")
}
