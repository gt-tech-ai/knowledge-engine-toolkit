package unit_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"

	platformgrpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	grpcserver "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc"
)

// lbCalls is how many health checks the round-robin test fans across the backends.
const lbCalls = 6

// startCountingHealthServer starts a health server on a fresh port that counts the
// Check calls it serves, and returns its address and the counter.
func startCountingHealthServer(t *testing.T) (addr string, count *int) {
	t.Helper()

	served := 0
	srv := grpcserver.NewServer(
		grpcserver.DefaultServerConfig(),
		grpc.ChainUnaryInterceptor(func(
			ctx context.Context, req any, _ *grpc.UnaryServerInfo,
			handler grpc.UnaryHandler,
		) (any, error) {
			served++

			return handler(ctx, req)
		}),
	)
	grpcserver.RegisterHealthService(srv)

	lis, err := new(net.ListenConfig).Listen(t.Context(), textTCP, text127001)
	require.NoError(t, err)

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(srv.GracefulStop)

	return lis.Addr().String(), &served
}

// twoAddressResolver returns a manual resolver advertising exactly addr1 and addr2.
func twoAddressResolver(addr1, addr2 string) *manual.Resolver {
	res := manual.NewBuilderWithScheme("lbtest")
	res.InitialState(fixtures.Build(func(state *resolver.State) {
		state.Addresses = []resolver.Address{
			fixtures.Build(func(address *resolver.Address) { address.Addr = addr1 }),
			fixtures.Build(func(address *resolver.Address) { address.Addr = addr2 }),
		}
	}))

	return res
}

// TestNewClient_RoundRobinRotatesAcrossBackends tests the client load-balancing policy.
//
// Why this test is important:
//   - A gRPC client holds one connection per subchannel; with the default pick_first it
//     sends every call to the first resolved pod, so a service scaled from one to many
//     pods leaves the new pods idle. round_robin is what spreads calls across the pods a
//     headless Service resolves.
//
// What it tests:
//   - With two backends behind a manual resolver and round_robin, both backends serve
//     calls; with the default pick_first, only one does.
func TestNewClient_RoundRobinRotatesAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		check func(t *testing.T, count1, count2 int)
		name  string
		lb    platformgrpc.LoadBalancing
	}{
		{
			name: "round robin",
			lb:   platformgrpc.LoadBalancingRoundRobin,
			check: func(t *testing.T, count1, count2 int) {
				t.Helper()
				assert.Positive(t, count1, "round robin leaves no backend idle")
				assert.Positive(t, count2, "round robin leaves no backend idle")
			},
		},
		{
			name: "pick first",
			lb:   platformgrpc.LoadBalancingPickFirst,
			check: func(t *testing.T, count1, count2 int) {
				t.Helper()
				assert.NotEqual(t, count1 == 0, count2 == 0,
					"pick_first pins every call to one backend")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runLoadBalancingCase(t, testCase.lb, testCase.check)
		})
	}
}

// runLoadBalancingCase dials two counting backends with the policy lb, makes lbCalls
// health checks, and hands the per-backend served counts to check.
func runLoadBalancingCase(
	t *testing.T,
	policy platformgrpc.LoadBalancing,
	check func(t *testing.T, count1, count2 int),
) {
	t.Helper()

	addr1, count1 := startCountingHealthServer(t)
	addr2, count2 := startCountingHealthServer(t)

	cfg := platformgrpc.DefaultClientConfig("lbtest:///ignored")

	cfg.LoadBalancing = policy

	conn, err := platformgrpc.NewClient(
		cfg, grpc.WithResolvers(twoAddressResolver(addr1, addr2)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	for range lbCalls {
		_, checkErr := client.Check(
			context.Background(), new(healthpb.HealthCheckRequest),
		)
		require.NoError(t, checkErr)
	}

	check(t, *count1, *count2)
}

// TestNewClient_UnknownLoadBalancingFailsCoded tests the factory's unknown-kind guard.
//
// Why this test is important:
//   - A config typo in load_balancing must stop the process with a coded error, not boot
//     with a silently-ignored policy (charter: fail loudly on an unknown kind).
//
// What it tests:
//   - NewClient with an unknown load_balancing returns a coded InvalidInput error.
func TestNewClient_UnknownLoadBalancingFailsCoded(t *testing.T) {
	t.Parallel()

	cfg := platformgrpc.DefaultClientConfig("passthrough:///x")

	cfg.LoadBalancing = "bogus"

	_, err := platformgrpc.NewClient(cfg)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
