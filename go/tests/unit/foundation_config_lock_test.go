package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// TestLockConfigValidate tests the lock config default and every rejection
// branch of LockConfig.Validate.
//
// Why this test is important:
//   - lock.* is tuned per environment (local in dev, redis in staging/prod);
//     Validate is the load-time guard that a fat-fingered overlay — an unknown
//     backend, a non-positive lease, or a renew interval too close to the TTL
//     (which would let a live lease expire between renew ticks) — fails loudly
//     instead of producing a silently-broken lock.
//
// What it tests:
//   - DefaultLockConfig() is local / 30s / 10s and validates; kind=redis
//     validates; an unknown kind, a non-positive TTL or renew_interval, and a
//     renew_interval > ttl/3 are each rejected.
func TestLockConfigValidate(t *testing.T) {
	t.Parallel()

	def := infra.DefaultLockConfig()
	assert.Equal(
		t,
		"local",
		def.Kind,
		"default backend must be the in-process local lock",
	)
	assert.Equal(t, 30*time.Second, def.TTL, "default lease must stay 30s")
	assert.Equal(
		t,
		10*time.Second,
		def.RenewInterval,
		"default renew cadence must stay 10s",
	)
	require.NoError(t, def.Validate(), "the default lock config must validate")

	redis := infra.DefaultLockConfig()
	redis.Kind = "redis"
	require.NoError(t, redis.Validate(), "kind=redis must validate")

	badKind := infra.DefaultLockConfig()
	badKind.Kind = "zookeeper"
	require.Error(t, badKind.Validate(), "an unknown lock kind must be rejected")

	badTTL := infra.DefaultLockConfig()
	badTTL.TTL = 0
	require.Error(t, badTTL.Validate(), "a non-positive ttl must be rejected")

	badRenew := infra.DefaultLockConfig()
	badRenew.RenewInterval = 0
	require.Error(
		t,
		badRenew.Validate(),
		"a non-positive renew_interval must be rejected",
	)

	tooFast := infra.DefaultLockConfig()
	tooFast.RenewInterval = 20 * time.Second // > ttl/3 (30s/3 = 10s)
	assert.Error(
		t,
		tooFast.Validate(),
		"a renew_interval greater than ttl/3 must be rejected",
	)
}
