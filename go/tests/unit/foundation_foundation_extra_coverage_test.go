package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	schemares "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/reswire"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestReswire_NewRetrier_ErrorPaths tests that NewRetrier surfaces both a config
// unmarshal failure and a post-unmarshal validation failure as errors.
//
// Why this test is important:
//   - reswire is the single composition helper every repo/client/interceptor
//     provider uses to build a retrier from config; if a malformed or out-of-range
//     resilience.retry overlay were swallowed instead of erroring, the provider
//     would silently fall back to a half-built retrier at startup. These two error
//     branches are the guard that a bad overlay fails the composition root loudly.
//
// What it tests:
//   - When the overlay is present but UnmarshalKey fails, NewRetrier returns a
//     wrapped error; when unmarshalling yields an invalid RetryConfig (negative
//     max_retries), NewRetrier returns the validation error.
func TestReswire_NewRetrier_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("unmarshal error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		loader := mocks.NewMockConfigLoader(ctrl)
		loader.EXPECT().Get("resilience.retry").Return(map[string]any{"max_retries": 5})
		loader.EXPECT().
			UnmarshalKey("resilience.retry", gomock.Any()).
			Return(apperr.Sentinel("decode failure"))

		_, err := reswire.NewRetrier(loader)
		require.Error(t, err, "an unmarshal failure must be surfaced")
	})

	t.Run("validation error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		loader := mocks.NewMockConfigLoader(ctrl)
		loader.EXPECT().Get("resilience.retry").Return(map[string]any{"max_retries": -1})
		loader.EXPECT().
			UnmarshalKey("resilience.retry", gomock.Any()).
			DoAndReturn(func(_ string, target any) error {
				*target.(*schemares.RetryConfig) = schemares.RetryConfig{
					MaxRetries: -1,
					Multiplier: 2,
				}
				return nil
			})

		_, err := reswire.NewRetrier(loader)
		require.Error(t, err, "an invalid retry config must be rejected")
	})
}

// TestReswire_NewBreaker_ErrorPaths tests that NewBreaker surfaces both a config
// unmarshal failure and a post-unmarshal validation failure as errors.
//
// Why this test is important:
//   - NewBreaker is how every named call-site breaker is built from config; a
//     malformed or out-of-range resilience.circuit_breaker overlay must fail the
//     composition root rather than yield a mis-tuned breaker that trips wrongly in
//     production. These are the two error branches that enforce that.
//
// What it tests:
//   - When the overlay is present but UnmarshalKey fails, NewBreaker returns a
//     wrapped error; when unmarshalling yields an invalid BreakerConfig
//     (failure_ratio > 1), NewBreaker returns the validation error.
func TestReswire_NewBreaker_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("unmarshal error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		loader := mocks.NewMockConfigLoader(ctrl)
		loader.EXPECT().
			Get("resilience.circuit_breaker").
			Return(map[string]any{"failure_ratio": 0.5})
		loader.EXPECT().
			UnmarshalKey("resilience.circuit_breaker", gomock.Any()).
			Return(apperr.Sentinel("decode failure"))

		_, err := reswire.NewBreaker(loader, "test.breaker")
		require.Error(t, err, "an unmarshal failure must be surfaced")
	})

	t.Run("validation error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		loader := mocks.NewMockConfigLoader(ctrl)
		loader.EXPECT().
			Get("resilience.circuit_breaker").
			Return(map[string]any{"failure_ratio": 2})
		loader.EXPECT().
			UnmarshalKey("resilience.circuit_breaker", gomock.Any()).
			DoAndReturn(func(_ string, target any) error {
				*target.(*schemares.BreakerConfig) = schemares.BreakerConfig{
					FailureRatio: 2,
				}
				return nil
			})

		_, err := reswire.NewBreaker(loader, "test.breaker")
		require.Error(t, err, "an invalid breaker config must be rejected")
	})
}

// TestExponentialRetry_CustomIsRetryable tests that a configured IsRetryable
// predicate is honored by both Retry and RetryWithResult: a non-retryable error
// stops after a single attempt.
//
// Why this test is important:
//   - IsRetryable is how a caller declares which failures are worth retrying;
//     if the retrier ignored it (using the default classifier instead), a
//     caller-declared terminal error would be retried needlessly — amplifying load
//     against a downstream that has already failed hard.
//
// What it tests:
//   - With IsRetryable returning false, both Retry and RetryWithResult invoke the
//     operation exactly once and return the error (no retries).
func TestExponentialRetry_CustomIsRetryable(t *testing.T) {
	t.Parallel()

	terminal := apperr.Sentinel("terminal")
	r := exponential.New(exponential.Config{
		MaxRetries:      5,
		InitialInterval: time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2,
		MaxElapsedTime:  time.Second,
		IsRetryable:     func(error) bool { return false },
	})

	calls := 0
	err := r.Retry(context.Background(), func() error {
		calls++
		return terminal
	})
	require.Error(t, err, "Retry must return the terminal error")
	assert.Equal(t, 1, calls, "a non-retryable error must not be retried")

	resultCalls := 0
	_, err = exponential.RetryWithResult(context.Background(), r, func() (int, error) {
		resultCalls++
		return 0, terminal
	})
	require.Error(t, err, "RetryWithResult must return the terminal error")
	assert.Equal(t, 1, resultCalls, "a non-retryable error must not be retried")
}

// TestBulkheadKind_String tests that Kind.String renders each bulkhead kind by
// its stable label, degrading an unknown kind to its numeric form.
//
// Why this test is important:
//   - Kind.String feeds logs and metrics labels; the adaptive label in particular
//     was unexercised. An unhandled or mislabelled kind hides a mis-configured
//     bulkhead from an operator reading a dashboard, and a blank string for an
//     unknown kind would be undebuggable.
//
// What it tests:
//   - KindChannel renders "channel", KindAdaptive renders "adaptive", and an
//     out-of-range Kind renders "Kind(99)".
func TestBulkheadKind_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "channel", bulkhead.KindChannel.String())
	assert.Equal(t, "adaptive", bulkhead.KindAdaptive.String())
	assert.Equal(t, "Kind(99)", bulkhead.Kind(99).String())
}
