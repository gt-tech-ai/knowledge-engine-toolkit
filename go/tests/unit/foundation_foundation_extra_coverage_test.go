package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
)

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
