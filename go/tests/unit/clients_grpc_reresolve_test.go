package unit_test

import (
	"context"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"

	platformgrpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
)

// reresolveInterval is the short poll interval the re-resolution tests dial with.
const reresolveInterval = 10 * time.Millisecond

// mutableLookup is a platformgrpc.LookupHostFunc whose returned address set can change
// between calls, to simulate a headless Service gaining a pod on a scale-up.
type mutableLookup struct {
	hosts []string
	mutex sync.Mutex
}

// set replaces the address set the next lookup returns.
func (m *mutableLookup) set(hosts ...string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.hosts = append([]string(nil), hosts...)
}

// lookup returns the current address set, satisfying platformgrpc.LookupHostFunc.
func (m *mutableLookup) lookup(context.Context, string) ([]string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return append([]string(nil), m.hosts...), nil
}

// recordingClientConn is a minimal resolver.ClientConn mock that records the address sets
// a resolver pushes, so a test can observe re-resolution. It is the external-boundary
// double for gRPC's resolver.ClientConn.
type recordingClientConn struct {
	pushed [][]string
	mutex  sync.Mutex
}

// UpdateState records the joined addresses of one resolver state push.
func (c *recordingClientConn) UpdateState(state resolver.State) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	got := make([]string, len(state.Addresses))
	for index, address := range state.Addresses {
		got[index] = address.Addr
	}

	c.pushed = append(c.pushed, got)

	return nil
}

// ReportError is a no-op; the test drives lookups that never fail.
func (*recordingClientConn) ReportError(error) {}

// NewAddress is the deprecated address-push path, unused by the polling resolver.
func (*recordingClientConn) NewAddress([]resolver.Address) {}

// ParseServiceConfig is unused; the polling resolver pushes no service config.
func (*recordingClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return nil
}

// last returns the most recent address set pushed, or nil if none yet.
func (c *recordingClientConn) last() []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if len(c.pushed) == 0 {
		return nil
	}

	return append([]string(nil), c.pushed[len(c.pushed)-1]...)
}

// TestDNSPollingResolver_PicksUpAddedAddress tests periodic DNS re-resolution.
//
// Why this test is important:
//   - gRPC's built-in dns resolver re-resolves only when a connection fails, so a client
//     dialing a headless Service never sees a pod added by a pure scale-up until an
//     existing connection breaks. The polling resolver is what closes that gap, and a
//     Connect-over-net/http server cannot send a GOAWAY to force the reconnect.
//
// What it tests:
//   - The resolver pushes the initial address set, then after the lookup gains a second
//     address (a scale-up) re-resolves on its interval and pushes both addresses.
func TestDNSPollingResolver_PicksUpAddedAddress(t *testing.T) {
	t.Parallel()

	lookup := &mutableLookup{hosts: nil, mutex: sync.Mutex{}}
	lookup.set("10.0.0.1")

	conn := &recordingClientConn{pushed: nil, mutex: sync.Mutex{}}
	builder := platformgrpc.NewDNSPollingResolver(reresolveInterval, lookup.lookup)

	parsed, err := url.Parse(platformgrpc.DNSPollScheme + ":///headless:8090")
	require.NoError(t, err)

	var target resolver.Target

	target.URL = *parsed

	var opts resolver.BuildOptions

	res, err := builder.Build(target, conn, opts)
	require.NoError(t, err)
	t.Cleanup(res.Close)

	require.Eventually(t, func() bool {
		return slices.Equal(conn.last(), []string{"10.0.0.1:8090"})
	}, time.Second, reresolveInterval, "initial resolution reports the one pod")

	lookup.set("10.0.0.1", "10.0.0.2")

	require.Eventually(t, func() bool {
		return slices.Equal(conn.last(), []string{"10.0.0.1:8090", "10.0.0.2:8090"})
	}, time.Second, reresolveInterval, "re-resolution picks up the scaled-up pod")
}

// TestNewClient_ResolveIntervalRoundRobinServesCalls tests the ResolveInterval knob.
//
// Why this test is important:
//   - The product wires periodic re-resolution through ClientConfig.ResolveInterval;
//     a dial with it set must still produce a working round_robin client (target rewrite
//     to the dnspoll scheme and the polling resolver must not break ordinary dialing).
//
// What it tests:
//   - NewClient with round_robin and a positive ResolveInterval dialing dns:///<addr>
//     reaches the backend and its health checks succeed.
func TestNewClient_ResolveIntervalRoundRobinServesCalls(t *testing.T) {
	t.Parallel()

	addr, count := startCountingHealthServer(t)

	cfg := platformgrpc.DefaultClientConfig("dns:///" + addr)

	cfg.LoadBalancing = platformgrpc.LoadBalancingRoundRobin
	cfg.ResolveInterval = reresolveInterval

	conn, err := platformgrpc.NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	for range lbCalls {
		_, checkErr := client.Check(
			context.Background(), new(healthpb.HealthCheckRequest),
		)
		require.NoError(t, checkErr)
	}

	assert.Positive(t, *count, "resolve-interval round_robin client reaches the backend")
}
