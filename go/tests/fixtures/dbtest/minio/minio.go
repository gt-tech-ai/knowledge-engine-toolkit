// Package minio provides a testcontainers-based MinIO instance for integration
// tests. It mirrors the redis sibling package: start a container, wait until it
// is healthy, and expose the endpoint + static credentials so the caller can
// build any S3 client it needs. No AWS SDK dependency lives in this package.
package minio

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

const (
	// DefaultImage is the MinIO image NewTestMinIO starts unless WithImage overrides it:
	// Chainguard's free build, pinned by digest (a multi-arch index) because MinIO no
	// longer publishes its own images. The tag is informational; the digest is what
	// is pulled.
	DefaultImage = "cgr.dev/chainguard/minio:latest" +
		"@sha256:bd014394a80898e68c149f2311fdf8d5a2c2f3bb2c33b9327ae6d02b4b065ae1"
	// minioAPIPort is the container's S3 API port.
	minioAPIPort = "9000/tcp"

	// RootUser is the static access key the container is started with.
	RootUser = "minioadmin"
	// RootPassword is the static secret key the container is started with.
	RootPassword = "minioadmin"
)

// TestMinIO wraps a testcontainers MinIO instance. Callers receive the endpoint
// URL and use RootUser/RootPassword with their own S3 client to connect.
type TestMinIO struct {
	// container is the running MinIO testcontainer.
	container testcontainers.Container
	// endpoint is the resolved http://host:port S3 API URL.
	endpoint string
}

// options holds the settings an Option adjusts.
type options struct {
	// image is the container image to start.
	image string
}

// Option adjusts how NewTestMinIO starts the container.
type Option func(*options)

// WithImage starts image instead of DefaultImage, so a consumer can repin MinIO
// (a newer digest, a mirror) without waiting for an engine release. The image
// must accept `server /data` and serve /minio/health/ready on port 9000.
func WithImage(image string) Option {
	return func(o *options) { o.image = image }
}

// NewTestMinIO starts a MinIO container (DefaultImage unless WithImage overrides
// it) and waits up to 60 seconds for its readiness endpoint to report healthy.
func NewTestMinIO(ctx context.Context, opts ...Option) (*TestMinIO, error) {
	o := options{image: DefaultImage}
	for _, opt := range opts {
		opt(&o)
	}

	req := testcontainers.ContainerRequest{
		Image:        o.image,
		ExposedPorts: []string{minioAPIPort},
		Env: map[string]string{
			"MINIO_ROOT_USER":     RootUser,
			"MINIO_ROOT_PASSWORD": RootPassword,
		},
		Cmd: []string{"server", "/data"},
		WaitingFor: wait.ForHTTP("/minio/health/ready").
			WithPort(minioAPIPort).
			WithStartupTimeout(60 * time.Second),
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
			"failed to start MinIO container from "+o.image,
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

	port, err := container.MappedPort(ctx, minioAPIPort)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}

	return &TestMinIO{
		container: container,
		endpoint:  fmt.Sprintf("http://%s:%s", host, port.Port()),
	}, nil
}

// Endpoint returns the MinIO S3 API endpoint URL (http://host:port).
func (m *TestMinIO) Endpoint() string {
	return m.endpoint
}

// Close terminates the MinIO container. Safe to call on a nil receiver.
func (m *TestMinIO) Close(ctx context.Context) {
	if m != nil && m.container != nil {
		m.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}
