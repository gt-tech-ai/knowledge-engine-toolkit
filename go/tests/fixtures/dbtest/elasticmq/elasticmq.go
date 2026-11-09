// Package elasticmq provides a testcontainers-based ElasticMQ (SQS-compatible)
// instance for integration tests. It mirrors the redis/postgres siblings: start a
// container, wait for it to accept connections, and expose the endpoint URL so the
// caller can build any SQS client it needs. No SQS client dependency lives here.
package elasticmq

import (
	"context"
	"net"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// startupTimeout bounds the container's start.
const startupTimeout = 60 * time.Second

const (
	// elasticmqImage is the ElasticMQ container image the fixture starts.
	elasticmqImage = "softwaremill/elasticmq:1.6.6"
	// elasticmqPort is the SQS-compatible port exposed by the container.
	elasticmqPort = "9324/tcp"

	// AccessKey is the static access key ID integration tests pass to an SQS
	// client. ElasticMQ does not validate credentials, but the AWS SDK requires
	// non-empty ones; mirrors the minio sibling's RootUser.
	AccessKey = "local"
	// SecretKey is the static secret access key paired with AccessKey.
	SecretKey = "local"
)

// TestElasticMQ wraps a testcontainers ElasticMQ instance. Callers receive only
// the endpoint URL and use their own SQS client (e.g. aws-sdk-go-v2) to connect.
type TestElasticMQ struct {
	// container is the running ElasticMQ testcontainer (nil after Close).
	container testcontainers.Container
	// endpoint is the SQS-compatible endpoint URL (http://host:port).
	endpoint string
}

// NewTestElasticMQ creates an ElasticMQ container and waits up to 60 seconds for
// the SQS port to be reachable.
func NewTestElasticMQ(ctx context.Context) (*TestElasticMQ, error) {
	var containerRequest testcontainers.ContainerRequest

	containerRequest.Image = elasticmqImage
	containerRequest.ExposedPorts = []string{elasticmqPort}
	containerRequest.WaitingFor = wait.ForListeningPort(elasticmqPort).
		WithStartupTimeout(startupTimeout)

	req := containerRequest

	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
			ProviderType:     0,
			Logger:           nil,
			Reuse:            false,
		},
	)
	if err != nil {
		// A failed start (e.g. a readiness timeout) can still leave a container behind;
		// TerminateContainer is nil-safe.
		_ = testcontainers.TerminateContainer(container)

		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to start ElasticMQ container",
		)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx) // best-effort cleanup on setup failure

		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to get container host",
		)
	}

	port, err := container.MappedPort(ctx, elasticmqPort)
	if err != nil {
		_ = container.Terminate(ctx) // best-effort cleanup on setup failure

		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}

	return &TestElasticMQ{
		container: container,
		endpoint:  "http://" + net.JoinHostPort(host, port.Port()),
	}, nil
}

// Endpoint returns the SQS-compatible endpoint URL (http://host:port).
func (e *TestElasticMQ) Endpoint() string {
	return e.endpoint
}

// Close terminates the ElasticMQ container. Safe to call on a nil receiver.
func (e *TestElasticMQ) Close(ctx context.Context) {
	if e != nil && e.container != nil {
		_ = e.container.Terminate(ctx) // best-effort cleanup on teardown
	}
}
