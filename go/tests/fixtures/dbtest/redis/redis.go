// Package redis provides a testcontainers-based Redis instance for integration
// tests. It mirrors the postgres sibling package: start a container, wait for
// it to accept connections, and expose the address so the caller can build any
// Redis client it needs. No Redis client dependency lives in this package.
package redis

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

const (
	// redisImage is the Redis container image the fixture starts.
	redisImage = "redis:7-alpine"
	// redisPort is the Redis port exposed by the container.
	redisPort = "6379/tcp"
)

// redisAddrEnv, when set (e.g. TEST_REDIS_ADDR=localhost:6379), makes NewTestRedis
// target an already-running Redis instead of starting a throwaway container. It is the
// opt-in "real dependency via a running compose service" path the system-E2E plan
// sanctions for the lock/backplane suites: on a Docker host that cannot reliably start
// new testcontainers (a resource-starved daemon), the compose stack's Redis is already
// serving, so the integration tests run against it with zero new container starts. Unset
// (the default, incl. CI), NewTestRedis uses testcontainers exactly as before.
const redisAddrEnv = "TEST_REDIS_ADDR"

// TestRedis wraps a testcontainers Redis instance. Callers receive only the
// host:port address and use their own client (e.g., go-redis) to connect.
type TestRedis struct {
	// container is the running Redis testcontainer (nil when TEST_REDIS_ADDR
	// targets an already-running instance, making Close a no-op).
	container testcontainers.Container
	// addr is the Redis server address in host:port format.
	addr string
}

// NewTestRedis creates a redis:7-alpine container and waits up to 30 seconds
// for port 6379 to be reachable. When TEST_REDIS_ADDR is set it skips the
// container entirely and returns a handle pointing at that already-running
// address (container is nil, so Close is a no-op).
func NewTestRedis(ctx context.Context) (*TestRedis, error) {
	if addr := os.Getenv(redisAddrEnv); addr != "" {
		return &TestRedis{addr: addr}, nil
	}

	req := testcontainers.ContainerRequest{
		Image:        redisImage,
		ExposedPorts: []string{redisPort},
		WaitingFor: wait.ForListeningPort(redisPort).
			WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		},
	)
	if err != nil {
		// A failed start (e.g. a readiness timeout) can still leave a container behind;
		// TerminateContainer is nil-safe.
		_ = testcontainers.TerminateContainer(container)
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to start Redis container",
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

	port, err := container.MappedPort(ctx, redisPort)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}

	return &TestRedis{
		container: container,
		addr:      fmt.Sprintf("%s:%s", host, port.Port()),
	}, nil
}

// Addr returns the Redis server address in host:port format.
func (r *TestRedis) Addr() string {
	return r.addr
}

// Close terminates the Redis container. Safe to call on a nil receiver.
func (r *TestRedis) Close(ctx context.Context) {
	if r != nil && r.container != nil {
		r.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}
