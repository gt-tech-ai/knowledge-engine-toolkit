package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/transport"
)

// TestTransportDefaults_Permissive tests that the transport config defaults are
// permissive until a service opts in: the rate limiter stays disabled and CORS
// stays open.
//
// Why this test is important:
//   - A broad edge-config surface must not restrict anything by default. If the rate
//     limiter defaulted enabled, or CORS defaulted restrictive, adopting the surface
//     would silently break every deployment (429s / blocked origins) before any
//     overlay opted in.
//
// What it tests:
//   - RateLimit.Enabled is false, CORS.AllowOrigins is ["*"], WS.IdleTimeout is 5m,
//     and the defaults validate.
func TestTransportDefaults_Permissive(t *testing.T) {
	t.Parallel()

	c := transport.DefaultConfig()
	assert.False(t, c.RateLimit.Enabled, "the rate limiter must stay disabled by default")
	assert.Equal(
		t,
		[]string{"*"},
		c.CORS.AllowOrigins,
		"CORS must stay permissive by default",
	)
	assert.Equal(t, 5*time.Minute, c.WS.IdleTimeout, "WS idle must default to 5m")
	require.NoError(t, c.Validate(), "the defaults must validate")
}

// TestTransportValidate_RejectsEnabledZeroRate tests that Validate rejects an
// enabled rate limiter with a non-positive rate.
//
// Why this test is important:
//   - Enabling the limiter with rate 0 would reject every request (a self-inflicted
//     outage); Validate must catch that fat-finger at config load, not at the
//     first request.
//
// What it tests:
//   - RateLimit{Enabled: true, Rate: 0} is rejected; a negative WS timeout is
//     rejected.
func TestTransportValidate_RejectsEnabledZeroRate(t *testing.T) {
	t.Parallel()

	bad := transport.DefaultConfig()
	bad.RateLimit.Enabled = true
	bad.RateLimit.Rate = 0
	require.Error(t, bad.Validate(), "enabled limiter with rate 0 must be rejected")

	badWS := transport.DefaultConfig()
	badWS.WS.IdleTimeout = -1
	assert.Error(t, badWS.Validate(), "a negative WS idle timeout must be rejected")
}

// TestTransportWS_HeartbeatDefaults tests the WebSocket liveness knobs on the
// transport.ws config surface: the ping cadence, the new pong deadline, and the read
// limit that the WS server threads into SetReadLimit instead of a hardcoded constant.
//
// Why this test is important:
//   - hardens the WS hub with heartbeat/idle/oversize behaviour; threads those timings
//     through config (ARCHITECTURE.md#configuration) rather than re-declaring package
//     constants. If PongTimeout defaulted to zero the heartbeat would wait forever for a
//     pong (a dead peer is never reaped), and if MaxMessageBytes stayed at the old 1 MiB
//     default the WS read limit would silently diverge from the intended 64 KiB
//     oversize→1009 cap.
//
// What it tests:
//   - DefaultConfig sets WS.PingInterval=30s, WS.PongTimeout=10s, and
//     WS.MaxMessageBytes=64 KiB; Validate rejects a negative pong timeout.
func TestTransportWS_HeartbeatDefaults(t *testing.T) {
	t.Parallel()

	c := transport.DefaultConfig()
	assert.Equal(
		t,
		30*time.Second,
		c.WS.PingInterval,
		"WS ping cadence must default to 30s",
	)
	assert.Equal(
		t,
		10*time.Second,
		c.WS.PongTimeout,
		"WS pong deadline must default to 10s",
	)
	assert.Equal(
		t,
		int64(64<<10),
		c.WS.MaxMessageBytes,
		"WS read limit must default to 64 KiB (the oversize→1009 cap)",
	)
	require.NoError(t, c.Validate(), "the WS defaults must validate")

	badPong := transport.DefaultConfig()
	badPong.WS.PongTimeout = -1
	assert.Error(t, badPong.Validate(), "a negative WS pong timeout must be rejected")
}
