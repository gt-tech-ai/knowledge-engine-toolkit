// Package cassandra provides a testcontainers-based Apache Cassandra 5 instance for
// integration tests. It mirrors the redis/postgres siblings: start a container,
// wait until cqlsh answers a query, and expose the host and port so the caller
// builds its own session. Schema bootstrap runs through cqlsh in the container, so
// no Cassandra driver dependency lives here.
package cassandra

import (
	"context"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

const (
	// cassandraImage is the Cassandra container image the fixture starts.
	cassandraImage = "cassandra:5"
	// cqlPort is the native-protocol port exposed by the container.
	cqlPort = "9042/tcp"
	// LocalDC is the datacenter name of the single-node cluster.
	LocalDC = "datacenter1"
	// startupTimeout bounds each readiness step and the whole wait; a cold
	// Cassandra 5 start on a CI runner takes longer than a minute.
	startupTimeout = 4 * time.Minute
)

// TestCassandra wraps a testcontainers Cassandra instance.
type TestCassandra struct {
	// container is the running Cassandra testcontainer (nil after Close).
	container testcontainers.Container
	// host is the container host reachable from the test process.
	host string
	// port is the mapped native-protocol port.
	port int
}

// NewTestCassandra starts a single-node Cassandra 5 with a small heap and waits
// (up to 4 minutes) until cqlsh can query system.local. Each wait step sets its
// own 4-minute timeout: a step without one falls back to its built-in 60s default
// (ForAll's WithStartupTimeoutDefault does not override it), which is shorter than
// a cold start on a CI runner.
func NewTestCassandra(ctx context.Context) (*TestCassandra, error) {
	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: cassandraRequest(),
			Started:          true,
			ProviderType:     0,
			Logger:           nil,
			Reuse:            false,
		},
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)

		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to start Cassandra container",
		)
	}

	host, err := hostIP(ctx, container)
	if err != nil {
		_ = container.Terminate(ctx) // best-effort cleanup on setup failure

		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to get container host",
		)
	}

	mapped, err := container.MappedPort(ctx, cqlPort)
	if err != nil {
		_ = container.Terminate(ctx) // best-effort cleanup on setup failure

		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}

	port, err := strconv.Atoi(mapped.Port())
	if err != nil {
		_ = container.Terminate(ctx) // best-effort cleanup on setup failure

		return nil, apperr.Wrap(err, apperr.CodeInternal, "invalid mapped port")
	}

	return &TestCassandra{container: container, host: host, port: port}, nil
}

// hostIP is the container host as an IP literal (IPv4 preferred): a session pinned
// to its contact point (cql's DisableHostDiscovery) needs an IP, and the host is
// usually the name "localhost".
func hostIP(ctx context.Context, container testcontainers.Container) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "container host")
	}

	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return "", apperr.Wrap(err, apperr.CodeInternal, "resolve container host "+host)
	}

	for _, addr := range addrs {
		if addr.To4() != nil {
			return addr.String(), nil
		}
	}

	return addrs[0].String(), nil
}

// Host returns the IP literal the test process connects to.
func (c *TestCassandra) Host() string { return c.host }

// Port returns the mapped native-protocol port.
func (c *TestCassandra) Port() int { return c.port }

// Exec runs one CQL statement through cqlsh inside the container (schema
// bootstrap); a non-zero exit is CodeInternal, carrying cqlsh's output.
func (c *TestCassandra) Exec(ctx context.Context, cql string) error {
	code, out, err := c.container.Exec(ctx, []string{"cqlsh", "-e", cql})
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "cqlsh exec failed")
	}

	if code != 0 {
		text, _ := io.ReadAll(out)

		return apperr.New(apperr.CodeInternal, "cqlsh: "+string(text))
	}

	return nil
}

// RestartNativeTransport stops and restarts the node's CQL listener with nodetool
// (disablebinary, enablebinary): every client connection drops, so a driver
// reconnects its control connection and refreshes its view of the ring, as it does
// when a node restarts. A failed or non-zero nodetool is CodeInternal.
func (c *TestCassandra) RestartNativeTransport(ctx context.Context) error {
	for _, sub := range []string{"disablebinary", "enablebinary"} {
		code, out, err := c.container.Exec(ctx, []string{"nodetool", sub})
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "nodetool "+sub+" failed")
		}

		if code != 0 {
			text, _ := io.ReadAll(out)

			return apperr.New(apperr.CodeInternal, "nodetool "+sub+": "+string(text))
		}
	}

	return nil
}

// Close terminates the container. Safe to call on a nil receiver.
func (c *TestCassandra) Close(ctx context.Context) {
	if c != nil && c.container != nil {
		_ = c.container.Terminate(ctx) // best-effort cleanup on teardown
	}
}

// cassandraRequest describes the single-node container and how to tell it is ready.
func cassandraRequest() testcontainers.ContainerRequest {
	var containerRequest testcontainers.ContainerRequest

	containerRequest.Image = cassandraImage
	containerRequest.ExposedPorts = []string{cqlPort}
	containerRequest.Env = map[string]string{
		"MAX_HEAP_SIZE":             "768M",
		"HEAP_NEWSIZE":              "128M",
		"CASSANDRA_DC":              LocalDC,
		"CASSANDRA_ENDPOINT_SNITCH": "GossipingPropertyFileSnitch",
	}
	containerRequest.WaitingFor = wait.ForAll(
		wait.ForListeningPort(cqlPort).WithStartupTimeout(startupTimeout),
		wait.ForExec([]string{"cqlsh", "-e", "SELECT release_version FROM system.local"}).
			WithExitCodeMatcher(func(code int) bool { return code == 0 }).
			WithStartupTimeout(startupTimeout),
	).WithDeadline(startupTimeout)

	return containerRequest
}
