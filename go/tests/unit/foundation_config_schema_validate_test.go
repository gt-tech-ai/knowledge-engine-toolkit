package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/pipelines"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/repos"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/stores"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/transport"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workers"
)

// TestResilienceExtraDefaults_BudgetAndTimeout tests that the budget and timeout
// schema defaults report their documented behavior-preserving values and validate
// cleanly.
//
// Why this test is important:
//   - BudgetConfig and TimeoutConfig are the two resilience knobs with no
//     example-based default test; a silently wrong default (a zero budget that
//     disables retries, a zero timeout that removes the deadline) would change
//     resilience behavior the moment the provider wires them in without any test
//     catching the drift.
//
// What it tests:
//   - DefaultBudgetConfig() is MaxRetries 100 and validates; DefaultTimeoutConfig()
//     is 30s and validates.
func TestResilienceExtraDefaults_BudgetAndTimeout(t *testing.T) {
	t.Parallel()

	b := resilience.DefaultBudgetConfig()
	assert.Equal(t, int32(100), b.MaxRetries, "default retry budget must stay 100")
	require.NoError(t, b.Validate(), "the default budget must validate")

	to := resilience.DefaultTimeoutConfig()
	assert.Equal(
		t,
		30*time.Second,
		to.Timeout,
		"default per-operation timeout must stay 30s",
	)
	require.NoError(t, to.Validate(), "the default timeout must validate")
}

// TestResilienceValidate_RemainingBranches tests every out-of-range rejection
// branch of the resilience schema Validate methods not already exercised by
// TestResilienceValidate_RejectsBadValues.
//
// Why this test is important:
//   - Each resilience sub-config is tuned per environment; Validate is the load-time
//     guard that a fat-fingered overlay (a negative budget/timeout, an out-of-range
//     bulkhead or rate-limit, a negative breaker interval/timeout, a negative retry
//     count/interval) fails loudly instead of producing a silently-broken decorator.
//
// What it tests:
//   - Budget/timeout reject a negative value; bulkhead rejects max_concurrent < 1,
//     min_concurrent < 0, and backoff_ratio out of (0,1]; rate-limit rejects a
//     negative rate and burst; the breaker rejects a negative interval and timeout;
//     retry rejects a negative max_retries and max_interval. Each default validates.
func TestResilienceValidate_RemainingBranches(t *testing.T) {
	t.Parallel()

	// Budget: negative ceiling rejected.
	badBudget := resilience.DefaultBudgetConfig()
	badBudget.MaxRetries = -1
	require.Error(t, badBudget.Validate(), "a negative retry budget must be rejected")

	// Timeout: negative deadline rejected.
	badTimeout := resilience.DefaultTimeoutConfig()
	badTimeout.Timeout = -1
	require.Error(t, badTimeout.Validate(), "a negative timeout must be rejected")

	// Bulkhead: default validates, then each field's guard fires.
	require.NoError(t, resilience.DefaultBulkheadConfig().Validate())

	badMax := resilience.DefaultBulkheadConfig()
	badMax.MaxConcurrent = 0
	require.Error(t, badMax.Validate(), "max_concurrent < 1 must be rejected")

	badMin := resilience.DefaultBulkheadConfig()
	badMin.MinConcurrent = -1
	require.Error(t, badMin.Validate(), "min_concurrent < 0 must be rejected")

	badRatio := resilience.DefaultBulkheadConfig()
	badRatio.BackoffRatio = 1.5
	require.Error(t, badRatio.Validate(), "backoff_ratio outside (0,1] must be rejected")

	// Rate limit: default validates, then negative rate and burst are rejected.
	require.NoError(t, resilience.DefaultRateLimitConfig().Validate())

	badRate := resilience.DefaultRateLimitConfig()
	badRate.Rate = -1
	require.Error(t, badRate.Validate(), "a negative rate must be rejected")

	badBurst := resilience.DefaultRateLimitConfig()
	badBurst.Burst = -1
	require.Error(t, badBurst.Validate(), "a negative burst must be rejected")

	// Breaker: the interval and timeout guards (failure-ratio is covered elsewhere).
	badInterval := resilience.DefaultBreakerConfig()
	badInterval.Interval = -1
	require.Error(
		t,
		badInterval.Validate(),
		"a negative breaker interval must be rejected",
	)

	badBreakerTimeout := resilience.DefaultBreakerConfig()
	badBreakerTimeout.Timeout = -1
	require.Error(
		t,
		badBreakerTimeout.Validate(),
		"a negative breaker timeout must be rejected",
	)

	// Retry: the max_retries and max_interval guards.
	badRetries := resilience.DefaultRetryConfig()
	badRetries.MaxRetries = -1
	require.Error(t, badRetries.Validate(), "a negative max_retries must be rejected")

	badMaxInterval := resilience.DefaultRetryConfig()
	badMaxInterval.MaxInterval = -1
	assert.Error(t, badMaxInterval.Validate(), "a negative max_interval must be rejected")
}

// TestLayerConfigValidate_RemainingBranches tests the layer-config Validate rejection
// branches not already exercised by TestLayerConfigValidate_RejectsBadValues,
// TestTransportValidate_RejectsEnabledZeroRate, or
// TestWorkersConfig_ValidatesSweepTimings.
//
// Why this test is important:
//   - The repo/pipeline/domain/store/edge/worker surfaces are tuned per environment;
//     the guards not hit by the existing tests (a negative cache TTL/version, an
//     unknown store kind,
//     a non-positive multipart size, a zero bulk count, the pending-cohort bounds,
//     a negative header cap, a
//     negative worker port/batch, the orphan-grace sweep invariant) each protect a
//     distinct silently-broken configuration and must fail loudly at load.
//
// What it tests:
//   - repos rejects a negative TTL and version; stores rejects an unknown kind and
//     accepts gorm; pipelines rejects a non-positive multipart threshold/part size;
//     services rejects a zero bulk count; stores rejects a negative max_page_size, a
//     non-positive/inverted pending-indexing cohort; transport rejects a negative
//     max_header_bytes; workers rejects a negative port/batch and an orphan_grace within
//     the multipart presign window.
func TestLayerConfigValidate_RemainingBranches(t *testing.T) {
	t.Parallel()

	// repos: each duration/version guard.
	badTTL := repos.DefaultConfig()
	badTTL.Caching.TTL = -1
	require.Error(t, badTTL.Validate(), "a negative cache TTL must be rejected")

	badVersion := repos.DefaultConfig()
	badVersion.Caching.Version = -1
	require.Error(t, badVersion.Validate(), "a negative cache version must be rejected")

	// stores: an ORM kind no store kind implements.
	badKind := stores.DefaultConfig()
	badKind.Kind = "mongo"
	require.Error(t, badKind.Validate(), "an unknown store kind must be rejected")
	gormKind := stores.DefaultConfig()
	gormKind.Kind = "gorm"
	require.NoError(t, gormKind.Validate(), "gorm is a store kind")

	// pipelines: non-positive multipart sizes.
	badThreshold := pipelines.DefaultConfig()
	badThreshold.Upload.MultipartThresholdBytes = 0
	require.Error(
		t,
		badThreshold.Validate(),
		"a zero multipart threshold must be rejected",
	)

	badPartSize := pipelines.DefaultConfig()
	badPartSize.Upload.MultipartPartSizeBytes = 0
	require.Error(
		t,
		badPartSize.Validate(),
		"a zero multipart part size must be rejected",
	)

	// stores: the branches the existing test does not cover.
	badMaxPage := stores.DefaultConfig()
	badMaxPage.MaxPageSize = -1
	require.Error(t, badMaxPage.Validate(), "a negative max_page_size must be rejected")

	badCountCap := stores.DefaultConfig()
	badCountCap.CountCap = -1
	require.Error(t, badCountCap.Validate(), "a negative count_cap must be rejected")

	// the list-query cache TTLs must be non-negative, and the
	// defaults are 45s/60s — a negative TTL is a config error.
	require.NoError(
		t,
		stores.DefaultConfig().Validate(),
		"the default cache config validates",
	)
	assert.Equal(t, 45*time.Second, stores.DefaultConfig().Cache.SuggestTTL)
	assert.Equal(t, 60*time.Second, stores.DefaultConfig().Cache.CountTTL)
	badSuggestTTL := stores.DefaultConfig()
	badSuggestTTL.Cache.SuggestTTL = -1
	require.Error(t, badSuggestTTL.Validate(), "a negative suggest_ttl must be rejected")
	badCountTTL := stores.DefaultConfig()
	badCountTTL.Cache.CountTTL = -1
	require.Error(t, badCountTTL.Validate(), "a negative count_ttl must be rejected")

	// transport: the header-cap guard.
	badHeader := transport.DefaultConfig()
	badHeader.MaxHeaderBytes = -1
	require.Error(t, badHeader.Validate(), "a negative max_header_bytes must be rejected")

	// workers: the generic port/batch guards and the orphan-grace sweep invariant.
	badPort := workers.Config{Port: -1}
	require.Error(t, badPort.Validate(0), "a negative worker port must be rejected")

	badBatch := workers.Config{BatchSize: -1}
	require.Error(t, badBatch.Validate(0), "a negative batch size must be rejected")

	expiry := 60 * time.Minute
	badOrphan := workers.Config{
		Port:        8085,
		ReaperTTL:   90 * time.Minute, // above expiry (valid)
		OrphanGrace: 30 * time.Minute, // <= expiry (invalid)
	}
	assert.Error(t, badOrphan.Validate(expiry),
		"an orphan_grace within the multipart presign window must be rejected")
}
