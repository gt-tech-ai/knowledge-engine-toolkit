package unit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	connectpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestConnectServer_DefaultServerConfig tests that DefaultServerConfig returns
// sensible non-zero defaults bound to the requested port.
//
// Why this test is important:
//   - Services that omit explicit server config inherit these defaults; zero
//     timeouts cause connection leaks under load in production
//
// What it tests:
//   - Port is set to the given value
//   - ReadTimeout, WriteTimeout, and ShutdownTimeout are sensible non-zero values
func TestConnectServer_DefaultServerConfig(t *testing.T) {
	t.Parallel()

	cfg := connectpkg.DefaultServerConfig(8090)
	assert.Equal(t, 8090, cfg.Port)
	assert.Equal(t, 30*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
}

// TestConnectServer_NewServer tests that NewServer returns a non-nil server and
// a non-nil HTTP mux when constructed with a valid config and logger.
//
// Why this test is important:
//   - Every Connect service depends on NewServer; a nil server would panic at
//     the first route registration
//
// What it tests:
//   - NewServer returns a non-nil server
//   - Mux() returns a non-nil HTTP mux
func TestConnectServer_NewServer(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)

	srv := connectpkg.NewServer(cfg, logger)
	require.NotNil(t, srv, "expected non-nil server")

	mux := srv.Mux()
	require.NotNil(t, mux, "expected non-nil mux")
}

// TestConnectServer_Register tests that Register accepts a HandlerRegistration
// without panicking.
//
// Why this test is important:
//   - Register is the primary extension point for Connect service handlers;
//     a panic here would prevent any gRPC service from binding
//
// What it tests:
//   - Register with a valid path and handler completes without error
func TestConnectServer_Register(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)
	srv := connectpkg.NewServer(cfg, logger)

	srv.Register(connectpkg.HandlerRegistration{
		Path:    "/test.v1.Service/",
		Handler: http.NotFoundHandler(),
	})
}

// TestConnectServer_RegisterFunc tests that RegisterFunc binds a raw HTTP
// handler function without panicking.
//
// Why this test is important:
//   - RegisterFunc is used for custom HTTP endpoints (metrics, admin routes);
//     a broken registration would silently drop the handler
//
// What it tests:
//   - RegisterFunc with a valid path and handler func completes without error
func TestConnectServer_RegisterFunc(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)
	srv := connectpkg.NewServer(cfg, logger)

	srv.RegisterFunc("/custom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestConnectServer_ShutdownNilHTTPServer tests that Shutdown returns nil when
// called before Start, where the underlying httpServer is nil.
//
// Why this test is important:
//   - Shutdown may be deferred before Start returns (e.g., in signal handlers);
//     a nil-pointer panic would crash the process during graceful teardown
//
// What it tests:
//   - Shutdown() before Start() returns nil without panicking
func TestConnectServer_ShutdownNilHTTPServer(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)
	srv := connectpkg.NewServer(cfg, logger)

	// Shutdown before Start should return nil (httpServer is nil).
	assert.NoError(t, srv.Shutdown(), "Shutdown() before Start should return nil")
}

// TestConnectServer_HealthzHandler tests that the /healthz endpoint registered
// by NewServer returns HTTP 200 with body "ok".
//
// Why this test is important:
//   - Kubernetes liveness probes hit /healthz to determine if the pod is alive
//   - A broken health endpoint causes the orchestrator to kill healthy pods
//   - The handler must return exactly 200 + "ok" for the probe to pass
//
// What it tests:
//   - GET /healthz returns status 200 and response body "ok"
func TestConnectServer_HealthzHandler(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)
	srv := connectpkg.NewServer(cfg, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/healthz",
		http.NoBody,
	)
	srv.Mux().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	assert.Equal(t, "ok", string(body))
}

// TestConnectServer_ReadyzHandler tests that the /readyz endpoint registered
// by NewServer returns HTTP 200 with body "ok".
//
// Why this test is important:
//   - Kubernetes readiness probes hit /readyz to determine if the pod can serve traffic
//   - A broken readiness endpoint removes the pod from the Service load balancer
//   - The handler must return exactly 200 + "ok" for the probe to pass
//
// What it tests:
//   - GET /readyz returns status 200 and response body "ok"
func TestConnectServer_ReadyzHandler(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0)
	srv := connectpkg.NewServer(cfg, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/readyz",
		http.NoBody,
	)
	srv.Mux().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	assert.Equal(t, "ok", string(body))
}

// TestConnectServer_StartAndShutdown tests that Start listens on an
// OS-assigned port and Shutdown gracefully terminates the server.
//
// Why this test is important:
//   - Start + Shutdown is the core lifecycle that every Connect service depends on
//   - The graceful shutdown path (lines 145-148) creates a timeout context and calls
//     httpServer.Shutdown, which must complete without error
//   - A broken shutdown would leak goroutines, leave ports bound, or drop in-flight
//     requests
//
// What it tests:
//   - Start on port 0 binds and returns once listening (non-blocking); Shutdown drains
//     cleanly
func TestConnectServer_StartAndShutdown(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	cfg := connectpkg.DefaultServerConfig(0) // port 0 -> OS picks a free port
	srv := connectpkg.NewServer(cfg, logger)

	// Start binds the listener and serves in the background, returning once the
	// server is listening (a bind failure would surface here instead).
	require.NoError(
		t,
		srv.Start(context.Background()),
		"Start() should return once listening",
	)

	// Graceful shutdown drains the server cleanly.
	require.NoError(t, srv.Shutdown(), "Shutdown() should drain cleanly")
}

// TestConnectServer_LifecycleContract tests the server's interfaces.Lifecycle +
// health surface across its lifecycle: before Start it is live but not ready, and
// after Start it is ready; Stop is a no-op before Start and drains after.
//
// Why this test is important:
//   - A lifecycle.Manager and Kubernetes probes rely on Readiness reporting
//     not-ready until the listener is bound (so traffic is not routed to a pod that
//     is not listening) while Liveness stays up, and on Stop being safe to call
//     whether or not the server was started.
//
// What it tests:
//   - Before Start: Liveness passes, Readiness errors, Stop is a no-op.
//   - After Start: Readiness passes; Stop drains cleanly.
func TestConnectServer_LifecycleContract(t *testing.T) {
	t.Parallel()

	srv := connectpkg.NewServer(connectpkg.DefaultServerConfig(0), fixtures.NopLogger())
	ctx := context.Background()

	require.NoError(t, srv.Liveness(ctx), "the process is up")
	require.Error(t, srv.Readiness(ctx), "not ready until Start binds the listener")
	assert.NoError(t, srv.Stop(ctx), "Stop before Start is a no-op")

	require.NoError(t, srv.Start(ctx), "Start binds and returns once listening")
	assert.NoError(t, srv.Readiness(ctx), "ready once the listener is bound")
	assert.NoError(t, srv.Stop(ctx), "Stop drains the running server cleanly")
}

// TestConnectServer_StartBindError tests that Start surfaces a bind failure instead
// of serving in the background.
//
// Why this test is important:
//   - Start binds the listener synchronously so a bind failure (e.g. an in-use or
//     invalid port) fails fast at the composition root rather than being swallowed by
//     the background serve goroutine.
//
// What it tests:
//   - Start against an out-of-range port returns a non-nil error.
func TestConnectServer_StartBindError(t *testing.T) {
	t.Parallel()

	// 999999 is outside the valid TCP port range, so net.Listen fails.
	srv := connectpkg.NewServer(
		connectpkg.DefaultServerConfig(999999),
		fixtures.NopLogger(),
	)
	require.Error(t, srv.Start(context.Background()), "an invalid port must fail Start")

	// Regression: after a failed bind the server must NOT report ready — the httpServer
	// is published only once net.Listen succeeds, so a K8s readiness probe cannot route
	// traffic to a pod whose listener never bound.
	assert.Error(
		t,
		srv.Readiness(context.Background()),
		"a server that failed to bind must not be ready",
	)
}
