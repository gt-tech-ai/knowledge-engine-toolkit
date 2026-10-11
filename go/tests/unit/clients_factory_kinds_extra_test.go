package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
)

// TestClientKind_StringFallbacks tests the tier Kind String fallbacks for an
// out-of-range value.
//
// Why this test is important:
//   - Kind strings appear in config-validation errors and logs; an unknown value must
//     render a diagnosable Kind(N) rather than an empty string, so a misconfiguration
//     is legible to an operator.
//
// What it tests:
//   - An out-of-range storage Kind stringifies via the numeric fallback.
func TestClientKind_StringFallbacks(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Kind(99)", storage.Kind(99).String())
}

// TestJobs_NewFromConfig_UnknownKind tests that the jobs factory fails loudly on an
// unknown backend kind.
//
// Why this test is important:
//   - NewFromConfig is the app-wiring entrypoint; an unknown kind must fail at
//     construction rather than silently returning a nil enqueuer that panics at first
//     enqueue.
//
// What it tests:
//   - jobs.NewFromConfig with an out-of-range Kind returns an error.
func TestJobs_NewFromConfig_UnknownKind(t *testing.T) {
	t.Parallel()
	_, err := jobs.NewFromConfig(jobs.Config{Kind: jobs.Kind(99)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown jobs kind")
}

// TestCache_NewFromConfig_RedisBackend tests that the cache factory builds the Redis
// backend (with TLS opted in) offline.
//
// Why this test is important:
//   - NewFromConfig is the app-wiring entrypoint for the cache tier; the Redis client
//     is built lazily (no connect), so a wiring or option-plumbing regression must
//     fail at construction, and TLS must be plumbed through for managed Redis.
//
// What it tests:
//   - cache.New(KindRedis, WithRedisTLS(true)) returns a non-nil ByteCache with no error.
func TestCache_NewFromConfig_RedisBackend(t *testing.T) {
	t.Parallel()
	c, err := cache.New(cache.KindRedis, cache.WithRedisTLS(true))
	require.NoError(t, err)
	require.NotNil(t, c)
}
