// Package foundation_test provides tests for resilience option functions,
// sub-package DefaultConfig functions, and RetryWithResult.

package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/channel"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	gb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker/gobreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter/token"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// ---------------------------------------------------------------------------
// Circuit Breaker option functions
// ---------------------------------------------------------------------------

// TestCircuitBreaker_OptionFunctions tests that each circuit breaker option
// function mutates the config correctly.
//
// Why this test is important:
//   - Functional options are the public API for customizing circuit breakers;
//     if an option silently fails, production services run with wrong thresholds
//   - Incorrect thresholds can either never trip (no protection) or always trip
//     (service unavailable)
//
// What it tests:
//   - WithName sets a custom name
//   - WithMaxRequests sets the half-open probe count
//   - WithInterval sets the reset interval
//   - WithTimeout sets the open-to-half-open timeout
//   - WithConsecutiveFailures sets the trip threshold
func TestCircuitBreaker_OptionFunctions(t *testing.T) {
	t.Parallel()

	cfg := circuitbreaker.DefaultConfig(textTest)
	circuitbreaker.WithName("custom")(&cfg)
	circuitbreaker.WithMaxRequests(5)(&cfg)
	circuitbreaker.WithInterval(30 * time.Second)(&cfg)
	circuitbreaker.WithTimeout(10 * time.Second)(&cfg)
	circuitbreaker.WithConsecutiveFailures(10)(&cfg)

	assert.Equal(t, "custom", cfg.Name)
	assert.Equal(t, uint32(wantMaxRequests), cfg.MaxRequests)
	assert.Equal(t, 30*time.Second, cfg.Interval)
	assert.Equal(t, 10*time.Second, cfg.Timeout)
	assert.Equal(t, uint32(wantConsecutiveFailures), cfg.ConsecutiveFailures)
}

// TestCircuitBreaker_ToOptions tests that Config.ToOptions produces options
// that faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop settings
//
// What it tests:
//   - ToOptions returns exactly 1 option
//   - Applying the option to a default config copies Name from the source
func TestCircuitBreaker_ToOptions(t *testing.T) {
	t.Parallel()

	src := fixtures.Build(func(row *circuitbreaker.Config) {
		row.Kind = circuitbreaker.KindGoBreaker
		row.Name = textSrc
		row.MaxRequests = maxRequests
		row.ConsecutiveFailures = consecutiveFailures
	})
	opts := src.ToOptions()
	require.Len(t, opts, 1, "expected 1 option from ToOptions")

	dst := circuitbreaker.DefaultConfig("default")
	for _, opt := range opts {
		opt(&dst)
	}

	assert.Equal(t, textSrc, dst.Name)
}

// TestCircuitBreaker_KindString tests that Kind.String() returns readable
// labels for known and unknown circuit breaker kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages and metrics labels; incorrect values
//     make debugging and alerting unreliable
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindGoBreaker.String() returns "gobreaker"
//   - An unknown Kind includes its numeric value in the string
func TestCircuitBreaker_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "gobreaker", circuitbreaker.KindGoBreaker.String())

	unknown := circuitbreaker.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

// TestGobreaker_DefaultConfig tests that the gobreaker sub-package provides its
// own sensible defaults.
//
// Why this test is important:
//   - The sub-package defaults are used when the parent factory delegates to
//     the gobreaker implementation; wrong defaults cascade to all consumers
//
// What it tests:
//   - Name matches the provided argument
//   - MaxRequests is non-zero
func TestGobreaker_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := gb.DefaultConfig("test-breaker")
	assert.Equal(t, "test-breaker", cfg.Name)
	assert.NotZero(t, cfg.MaxRequests, "expected non-zero MaxRequests in default config")
}

// ---------------------------------------------------------------------------
// Retry option functions
// ---------------------------------------------------------------------------

// TestRetry_OptionFunctions tests that each retry option function mutates the
// config correctly.
//
// Why this test is important:
//   - Functional options are the public API for tuning retry behavior; if an
//     option silently fails, services retry with wrong intervals or counts
//   - Incorrect retry config can cause either no retries or unbounded retries
//
// What it tests:
//   - WithMaxRetries sets the maximum retry count
//   - WithInitialInterval sets the first backoff duration
//   - WithMaxInterval sets the backoff ceiling
//   - WithMultiplier sets the exponential growth factor
//   - WithMaxElapsedTime sets the total retry time budget
func TestRetry_OptionFunctions(t *testing.T) {
	t.Parallel()

	cfg := retry.DefaultConfig()
	retry.WithMaxRetries(10)(&cfg)
	retry.WithInitialInterval(500 * time.Millisecond)(&cfg)
	retry.WithMaxInterval(30 * time.Second)(&cfg)
	retry.WithMultiplier(3.0)(&cfg)
	retry.WithMaxElapsedTime(2 * time.Minute)(&cfg)

	assert.Equal(t, wantMaxRetriesFunctions, cfg.MaxRetries)
	assert.Equal(t, 500*time.Millisecond, cfg.InitialInterval)
	assert.Equal(t, 30*time.Second, cfg.MaxInterval)
	assert.InDelta(t, wantMultiplier, cfg.Multiplier, 0)
	assert.Equal(t, 2*time.Minute, cfg.MaxElapsedTime)
}

// TestRetry_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop retry settings
//
// What it tests:
//   - Applying ToOptions to a default config copies MaxRetries from the source
func TestRetry_ToOptions(t *testing.T) {
	t.Parallel()

	src := fixtures.Build(func(row *retry.Config) {
		row.Kind = retry.KindExponential
		row.MaxRetries = maxRetries
	})
	opts := src.ToOptions()
	dst := retry.DefaultConfig()

	for _, opt := range opts {
		opt(&dst)
	}

	assert.Equal(t, wantMaxRetriesOptions, dst.MaxRetries)
}

// TestRetry_KindString tests that Kind.String() returns readable labels for
// known and unknown retry kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages and metrics labels; incorrect values
//     make debugging and alerting unreliable
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindExponential.String() returns "exponential"
//   - An unknown Kind includes its numeric value in the string
func TestRetry_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "exponential", retry.KindExponential.String())

	unknown := retry.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

// TestExponential_DefaultConfig tests that the exponential sub-package provides
// its own sensible defaults.
//
// Why this test is important:
//   - The sub-package defaults are used when the parent factory delegates to
//     the exponential implementation; wrong defaults cascade to all consumers
//
// What it tests:
//   - MaxRetries is 3
//   - Multiplier is 2.0
func TestExponential_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := exponential.DefaultConfig()
	assert.Equal(t, wantMaxRetriesConfig, cfg.MaxRetries)
	assert.InDelta(t, wantMultiplierConfig, cfg.Multiplier, 0)
}

// TestExponential_RetryWithResult tests that the generic RetryWithResult
// returns typed values on success and retries on transient failure.
//
// Why this test is important:
//   - RetryWithResult is used by retrieval and ingestion services to retry
//     operations that return values (e.g. Bedrock KB queries); the generic
//     wrapper must preserve both the value and error correctly
//
// What it tests:
//   - Immediate success returns ("ok", nil)
//   - Failure on first attempt followed by success on second returns ("recovered", nil)
func TestExponential_RetryWithResult(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      3,
		InitialInterval: 1 * time.Millisecond,
		MaxInterval:     10 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
		IsRetryable:     nil,
	})

	ctx := context.Background()

	// Success case
	result, err := exponential.RetryWithResult(ctx, retrier, func() (string, error) {
		return textOk, nil
	})
	require.NoError(t, err, "RetryWithResult must not return error on success")
	assert.Equal(t, textOk, result)

	// Failure case - succeeds on 2nd attempt
	attempt := 0

	result, err = exponential.RetryWithResult(ctx, retrier, func() (string, error) {
		attempt++
		if attempt < 2 {
			return "", apperr.Sentinel("transient")
		}

		return "recovered", nil
	})
	require.NoError(t, err, "RetryWithResult must not return error after recovery")
	assert.Equal(t, "recovered", result)
}

// ---------------------------------------------------------------------------
// Rate Limiter option functions
// ---------------------------------------------------------------------------

// TestRateLimiter_OptionFunctions tests that each rate limiter option function
// mutates the config correctly.
//
// Why this test is important:
//   - Functional options are the public API for tuning rate limits; if an
//     option silently fails, services throttle at wrong rates
//   - Incorrect rate config can either block all traffic or provide no
//     protection
//
// What it tests:
//   - WithRate sets the per-second token rate
//   - WithBurst sets the burst capacity
func TestRateLimiter_OptionFunctions(t *testing.T) {
	t.Parallel()

	cfg := ratelimiter.DefaultConfig()
	ratelimiter.WithRate(50.0)(&cfg)
	ratelimiter.WithBurst(5)(&cfg)

	assert.InDelta(t, wantRate, cfg.Rate, 0)
	assert.Equal(t, wantBurst, cfg.Burst)
}

// TestRateLimiter_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop rate settings
//
// What it tests:
//   - Applying ToOptions to a default config copies Rate from the source
func TestRateLimiter_ToOptions(t *testing.T) {
	t.Parallel()

	src := ratelimiter.Config{Kind: ratelimiter.KindToken, Rate: rate, Burst: burst}
	opts := src.ToOptions()
	dst := ratelimiter.DefaultConfig()

	for _, opt := range opts {
		opt(&dst)
	}

	assert.InDelta(t, wantRateOptions, dst.Rate, 0)
}

// TestRateLimiter_KindString tests that Kind.String() returns readable labels
// for known and unknown rate limiter kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages and metrics labels; incorrect values
//     make debugging and alerting unreliable
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindToken.String() returns "token"
//   - An unknown Kind includes its numeric value in the string
func TestRateLimiter_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "token", ratelimiter.KindToken.String())

	unknown := ratelimiter.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

// TestToken_DefaultConfig tests that the token sub-package provides its own
// sensible defaults.
//
// Why this test is important:
//   - The sub-package defaults are used when the parent factory delegates to
//     the token bucket implementation; zero values would block all requests
//
// What it tests:
//   - Rate is non-zero
//   - Burst is non-zero
func TestToken_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := token.DefaultConfig()
	assert.NotZero(t, cfg.Rate, "expected non-zero Rate in default config")
	assert.NotZero(t, cfg.Burst, "expected non-zero Burst in default config")
}

// ---------------------------------------------------------------------------
// Bulkhead option functions
// ---------------------------------------------------------------------------

// TestBulkhead_OptionFunctions tests that each bulkhead option function mutates
// the config correctly.
//
// Why this test is important:
//   - Functional options are the public API for tuning bulkhead concurrency;
//     if the option silently fails, services run with wrong concurrency limits
//
// What it tests:
//   - WithMaxConcurrent sets the concurrency limit
func TestBulkhead_OptionFunctions(t *testing.T) {
	t.Parallel()

	cfg := bulkhead.DefaultConfig()
	bulkhead.WithMaxConcurrent(20)(&cfg)

	assert.Equal(t, wantMaxConcurrent, cfg.MaxConcurrent)
}

// TestBulkhead_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop concurrency limits
//
// What it tests:
//   - Applying ToOptions to a default config copies MaxConcurrent from the source
func TestBulkhead_ToOptions(t *testing.T) {
	t.Parallel()

	src := fixtures.Build(func(row *bulkhead.Config) {
		row.Kind = bulkhead.KindChannel
		row.MaxConcurrent = maxConcurrent
	})
	opts := src.ToOptions()
	dst := bulkhead.DefaultConfig()

	for _, opt := range opts {
		opt(&dst)
	}

	assert.Equal(t, wantMaxConcurrentOptions, dst.MaxConcurrent)
}

// TestBulkhead_KindString tests that Kind.String() returns readable labels for
// known and unknown bulkhead kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages and metrics labels; incorrect values
//     make debugging and alerting unreliable
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindChannel.String() returns "channel"
//   - An unknown Kind includes its numeric value in the string
func TestBulkhead_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "channel", bulkhead.KindChannel.String())

	unknown := bulkhead.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

// TestChannel_DefaultConfig tests that the channel sub-package provides its own
// sensible defaults.
//
// Why this test is important:
//   - The sub-package defaults are used when the parent factory delegates to
//     the channel implementation; zero concurrency would block all operations
//
// What it tests:
//   - MaxConcurrent is 10
func TestChannel_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := channel.DefaultConfig()
	assert.Equal(t, wantMaxConcurrentConfig, cfg.MaxConcurrent)
}

// TestChannel_TryExecuteSuccess tests the TryExecute happy path when slots are
// available.
//
// Why this test is important:
//   - TryExecute is the non-blocking alternative to Execute; it must succeed
//     without error when capacity is available to avoid false rejections
//
// What it tests:
//   - TryExecute with a no-op function returns nil error
func TestChannel_TryExecuteSuccess(t *testing.T) {
	t.Parallel()

	bh := channel.New(channel.Config{MaxConcurrent: 2})
	err := bh.TryExecute(func() error { return nil })
	require.NoError(t, err, "TryExecute must not return error when capacity is available")
}

// TestChannel_ExecuteCancelledContext tests that Execute respects context
// cancellation when all bulkhead slots are occupied.
//
// Why this test is important:
//   - Request handlers carry deadlines; if Execute ignores context cancellation
//     when no slots are available, goroutines leak and requests hang
//   - Proper cancellation propagation is essential for graceful shutdown
//
// What it tests:
//   - Execute with a cancelled context returns a non-nil error when no slots are
//     available
func TestChannel_ExecuteCancelledContext(t *testing.T) {
	t.Parallel()

	// Fill all slots
	currentBulkhead := channel.New(channel.Config{MaxConcurrent: 1})
	blocker := make(chan struct{})

	go func() {
		_ = currentBulkhead.Execute(context.Background(), func() error {
			<-blocker

			return nil
		})
	}()

	time.Sleep(20 * time.Millisecond)

	// Try to execute with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := currentBulkhead.Execute(ctx, func() error { return nil })
	require.Error(
		t,
		err,
		"Execute must return error when context is cancelled and no slots available",
	)

	close(blocker)
}

// ---------------------------------------------------------------------------
// Tracer option functions
// ---------------------------------------------------------------------------

// TestTracer_OptionFunctions tests that each tracer option function mutates
// the config correctly.
//
// Why this test is important:
//   - Functional options are the public API for configuring distributed
//     tracing; if an option silently fails, traces go to the wrong collector
//     or are sampled incorrectly
//   - Incorrect service names make traces impossible to correlate across
//     services
//
// What it tests:
//   - WithServiceName sets the service name
//   - WithEndpoint sets the OTLP collector endpoint
//   - WithSampleRate sets the sampling ratio
//   - WithInsecure toggles TLS
func TestTracer_OptionFunctions(t *testing.T) {
	t.Parallel()

	cfg := tracer.DefaultConfig(textTest)
	tracer.WithServiceName("custom-service")(&cfg)
	tracer.WithEndpoint("otel.example.com:4317")(&cfg)
	tracer.WithSampleRate(0.5)(&cfg)
	tracer.WithInsecure(false)(&cfg)

	assert.Equal(t, "custom-service", cfg.ServiceName)
	assert.Equal(t, "otel.example.com:4317", cfg.Endpoint)
	assert.InDelta(t, wantSampleRateFunctions, cfg.SampleRate, 0)
	assert.False(t, cfg.Insecure, "Insecure should be false")
}

// TestTracer_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop tracer settings
//
// What it tests:
//   - Applying ToOptions to a default config copies ServiceName from the source
func TestTracer_ToOptions(t *testing.T) {
	t.Parallel()

	src := fixtures.Build(func(row *tracer.Config) {
		row.ServiceName = "src-svc"
		row.Endpoint = textLocalhost4317
	})
	opts := src.ToOptions()
	dst := tracer.DefaultConfig("default")

	for _, opt := range opts {
		opt(&dst)
	}

	assert.Equal(t, "src-svc", dst.ServiceName)
}

// TestTracer_KindString tests that Kind.String() returns readable labels for
// known and unknown tracer kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages and metrics labels; incorrect values
//     make debugging and alerting unreliable
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindOTel.String() returns "otel"
//   - An unknown Kind includes its numeric value in the string
func TestTracer_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "otel", tracer.KindOTel.String())

	unknown := tracer.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

// ---------------------------------------------------------------------------
// Config option functions
// ---------------------------------------------------------------------------

// TestConfig_ToOptions tests that the config loader's Config.ToOptions produces
// options that faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop config paths
//
// What it tests:
//   - Applying ToOptions to a default config copies BaseDir from the source
func TestConfig_ToOptions(t *testing.T) {
	t.Parallel()

	src := config.DefaultConfig()

	src.Viper.BaseDir = "/custom"

	opts := src.ToOptions()
	dst := config.DefaultConfig()

	for _, opt := range opts {
		opt(&dst)
	}

	assert.Equal(t, "/custom", dst.Viper.BaseDir)
}

// TestConfig_WithEnvPrefix tests that the WithEnvPrefix option sets the
// environment variable prefix for config binding.
//
// Why this test is important:
//   - Environment variable binding is the primary way to override config in
//     containers; a wrong prefix means env vars are silently ignored
//   - Multi-service deployments use distinct prefixes to avoid collisions
//
// What it tests:
//   - WithEnvPrefix("CUSTOM") sets Prefix to "CUSTOM"
func TestConfig_WithEnvPrefix(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	config.WithEnvPrefix("CUSTOM")(&cfg)
	assert.Equal(t, "CUSTOM", cfg.Viper.Prefix)
}

// TestConfig_KindString tests that Kind.String() returns readable labels for
// known and unknown config loader kinds.
//
// Why this test is important:
//   - Kind strings appear in log messages during startup; incorrect values
//     make it hard to identify which config backend is active
//
// What it tests:
//   - KindViper.String() returns "viper"
//   - An unknown Kind includes its numeric value in the string
func TestConfig_KindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "viper", config.KindViper.String())

	unknown := config.Kind(kindInput)
	assert.Contains(
		t,
		unknown.String(),
		text99,
		textUnknownKindShouldContain,
	)
}

const (
	// burst is the burst the tests use.
	burst = 3

	// consecutiveFailures is the consecutive failures the tests use.
	consecutiveFailures = 7

	// maxConcurrent is the max concurrent the tests use.
	maxConcurrent = 15

	// maxRequests is the max requests the tests use.
	maxRequests = 3

	// maxRetries is the max retries the tests use.
	maxRetries = 7

	// rate is the rate the tests use.
	rate = 42.0

	// wantBurst is the expected burst.
	wantBurst = 5

	// wantConsecutiveFailures is the expected consecutive failures.
	wantConsecutiveFailures = 10

	// wantMaxConcurrent is the expected max concurrent.
	wantMaxConcurrent = 20

	// wantMaxConcurrentOptions is the expected max concurrent options.
	wantMaxConcurrentOptions = 15

	// wantMaxRequests is the expected max requests.
	wantMaxRequests = 5

	// wantMaxRetriesFunctions is the expected max retries functions.
	wantMaxRetriesFunctions = 10

	// wantMaxRetriesOptions is the expected max retries options.
	wantMaxRetriesOptions = 7

	// wantMultiplier is the expected multiplier.
	wantMultiplier = 3.0

	// wantRate is the expected rate.
	wantRate = 50.0

	// wantRateOptions is the expected rate options.
	wantRateOptions = 42.0

	// wantSampleRateFunctions is the expected sample rate functions.
	wantSampleRateFunctions = 0.5
)
