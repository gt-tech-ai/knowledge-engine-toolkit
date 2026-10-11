package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// TestReplayConfigDefaults tests the WebSocket reconnect-replay buffer config defaults
// and validation.
//
// Why this test is important:
//   - The buffer is a memory-exhaustion vector if unbounded and useless if it never
//     expires; the defaults (100 messages, 5-minute window, 500ms op timeout) are the
//     safe recovery envelope, and Validate must reject a fat-fingered zero bound/ttl
//     before startup rather than at runtime.
//
// What it tests:
//   - DefaultReplayConfig is MaxSize=100, TTL=5m, OpTimeout=500ms and validates; a zero
//     MaxSize or TTL is rejected.
func TestReplayConfigDefaults(t *testing.T) {
	t.Parallel()

	c := infra.DefaultReplayConfig()
	assert.Equal(t, 100, c.MaxSize)
	assert.Equal(t, 5*time.Minute, c.TTL)
	assert.Equal(t, 500*time.Millisecond, c.OpTimeout)
	require.NoError(t, c.Validate())

	badSize := infra.DefaultReplayConfig()
	badSize.MaxSize = 0
	require.Error(t, badSize.Validate(), "a non-positive max_size must be rejected")

	badTTL := infra.DefaultReplayConfig()
	badTTL.TTL = 0
	assert.Error(t, badTTL.Validate(), "a non-positive ttl must be rejected")
}
