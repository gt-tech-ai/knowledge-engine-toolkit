package grpc

import (
	"context"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/resolver"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// DNSPollScheme is the gRPC target scheme served by the periodic re-resolving DNS
// resolver. NewClient rewrites a round_robin target to this scheme when ResolveInterval
// is positive; a caller building its own target may pair it with NewDNSPollingResolver.
const DNSPollScheme = "dnspoll"

// LookupHostFunc resolves a host name to its addresses, with *net.Resolver.LookupHost's
// own signature so net.DefaultResolver satisfies it unchanged. It is the DNS boundary the
// polling resolver depends on, injected so a test can drive resolution without real DNS.
type LookupHostFunc func(ctx context.Context, host string) ([]string, error)

// NewDNSPollingResolver returns a resolver.Builder, for grpc.WithResolvers, that re-runs
// a DNS lookup of the target host every interval and pushes the resolved address set to
// gRPC, so a round_robin client dialing a headless Kubernetes Service picks up pods added
// by a scale-up without waiting for a connection to break. gRPC's built-in dns resolver
// re-resolves only on a connection failure, so a pure scale-up is otherwise never seen. A
// nil lookup uses net.DefaultResolver.LookupHost; interval must be positive.
func NewDNSPollingResolver(
	interval time.Duration,
	lookup LookupHostFunc,
) resolver.Builder {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}

	return &dnsPollingBuilder{lookup: lookup, interval: interval}
}

// dnsPollingBuilder builds dnsPollingResolver instances for the DNSPollScheme target
// scheme. One builder is created per dial and passed via grpc.WithResolvers, so it holds
// no process-global state.
type dnsPollingBuilder struct {
	lookup   LookupHostFunc
	interval time.Duration
}

// Build parses the target's host:port endpoint and starts a dnsPollingResolver, which
// runs its first resolution immediately.
func (b *dnsPollingBuilder) Build(
	target resolver.Target,
	clientConn resolver.ClientConn,
	_ resolver.BuildOptions,
) (resolver.Resolver, error) {
	host, port, err := net.SplitHostPort(target.Endpoint())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
			"dnspoll resolver: target endpoint must be host:port")
	}

	ctx, cancel := context.WithCancel(context.Background())
	res := &dnsPollingResolver{
		clientConn: clientConn,
		lookup:     b.lookup,
		trigger:    make(chan struct{}, 1),
		cancel:     cancel,
		host:       host,
		port:       port,
		waitGroup:  sync.WaitGroup{},
	}

	res.waitGroup.Add(1)

	go res.watch(ctx, b.interval)

	return res, nil
}

// Scheme returns the target scheme this builder serves.
func (*dnsPollingBuilder) Scheme() string { return DNSPollScheme }

// dnsPollingResolver re-resolves its host on a timer and on ResolveNow, pushing each
// resolved address set to gRPC through clientConn until it is closed.
type dnsPollingResolver struct {
	clientConn resolver.ClientConn
	lookup     LookupHostFunc
	trigger    chan struct{}
	cancel     context.CancelFunc
	host       string
	port       string
	waitGroup  sync.WaitGroup
}

// ResolveNow asks for an immediate re-resolution; gRPC calls it when a subchannel fails.
func (r *dnsPollingResolver) ResolveNow(resolver.ResolveNowOptions) {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Close stops the watch goroutine and waits for it to return.
func (r *dnsPollingResolver) Close() {
	r.cancel()
	r.waitGroup.Wait()
}

// watch resolves once immediately, then on every interval tick and every ResolveNow,
// until the resolver is closed.
func (r *dnsPollingResolver) watch(ctx context.Context, interval time.Duration) {
	defer r.waitGroup.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		r.resolve(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.trigger:
		}
	}
}

// resolve runs one DNS lookup and pushes the resulting host:port address set to gRPC,
// reporting a lookup or update error to the connection so gRPC can retry.
func (r *dnsPollingResolver) resolve(ctx context.Context) {
	hosts, err := r.lookup(ctx, r.host)
	if err != nil {
		r.clientConn.ReportError(apperr.Wrap(err, apperr.CodeUnavailable,
			"dnspoll resolver: lookup "+r.host))

		return
	}

	// A zero-value declaration, not a composite literal, keeps exhaustruct satisfied
	// without naming resolver.Address's deprecated BalancerAttributes / Metadata fields
	// (staticcheck SA1019); only Addr is set.
	addresses := make([]resolver.Address, len(hosts))
	for index, resolved := range hosts {
		var address resolver.Address

		address.Addr = net.JoinHostPort(resolved, r.port)
		addresses[index] = address
	}

	var state resolver.State

	state.Addresses = addresses

	err = r.clientConn.UpdateState(state)
	if err != nil {
		r.clientConn.ReportError(apperr.Wrap(err, apperr.CodeUnavailable,
			"dnspoll resolver: update state"))
	}
}
