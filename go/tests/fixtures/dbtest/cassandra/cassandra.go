// Package cassandra provides a testcontainers-based Apache Cassandra 5 instance for
// integration tests. It mirrors the redis/postgres siblings: start a container,
// wait until cqlsh answers a query, and expose the host and port so the caller
// builds its own session. Schema bootstrap runs through cqlsh in the container, so
// no Cassandra driver dependency lives here.
package cassandra

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	req := testcontainers.ContainerRequest{
		Image:        cassandraImage,
		ExposedPorts: []string{cqlPort},
		Env: map[string]string{
			"MAX_HEAP_SIZE":             "768M",
			"HEAP_NEWSIZE":              "128M",
			"CASSANDRA_DC":              LocalDC,
			"CASSANDRA_ENDPOINT_SNITCH": "GossipingPropertyFileSnitch",
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(cqlPort).WithStartupTimeout(startupTimeout),
			wait.ForExec([]string{"cqlsh", "-e", "SELECT release_version FROM system.local"}).
				WithExitCodeMatcher(func(code int) bool { return code == 0 }).
				WithStartupTimeout(startupTimeout),
		).WithDeadline(startupTimeout),
	}
	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
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
	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to get container host",
		)
	}
	mapped, err := container.MappedPort(ctx, cqlPort)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}
	port, err := strconv.Atoi(mapped.Port())
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(err, apperr.CodeInternal, "invalid mapped port")
	}
	return &TestCassandra{container: container, host: host, port: port}, nil
}

// Host returns the host the test process connects to.
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

// Close terminates the container. Safe to call on a nil receiver.
func (c *TestCassandra) Close(ctx context.Context) {
	if c != nil && c.container != nil {
		c.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}
