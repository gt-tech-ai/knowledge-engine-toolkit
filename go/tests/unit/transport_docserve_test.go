package unit_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/docserve"
)

// docserveCfg is a docserve config over root listening on apiPort, with the specs at
// services/<svc>/docs/openapi.json and the Python packages under python/.
func docserveCfg(root, apiPort string) docserve.Config {
	return docserve.Config{
		Root: root, ServerDir: textServices, OpenAPISpec: "docs/openapi.json",
		PkgsiteHint: "go install pkgsite", GoPort: "16060", PythonPort: "16061",
		APIPort: apiPort, PythonRoots: []string{textPython},
	}
}

// docserveDeps returns deps with runner and sup, a no-op logger and discarded streams.
func docserveDeps(
	runner interfaces.CommandRunner,
	sup interfaces.Supervisor,
) docserve.Deps {
	return docserve.Deps{
		Runner: runner, Supervisor: sup, Log: fixtures.NopLogger(),
		Streams: types.Streams{Out: io.Discard, Err: io.Discard},
	}
}

// freePort returns a TCP port free at the time of the call.
func freePort(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), textTCP, text127001)
	require.NoError(t, err)

	port := asType[*net.TCPAddr](t, ln.Addr()).Port
	require.NoError(t, ln.Close())

	return strconv.Itoa(port)
}

// writeSpec writes a minimal OpenAPI spec for svc under root.
func writeSpec(t *testing.T, root, svc string) {
	t.Helper()

	dir := filepath.Join(root, textServices, svc, textDocsLower)
	require.NoError(t, os.MkdirAll(dir, mkdirAllInputSpec))
	require.NoError(t, os.WriteFile(filepath.Join(dir, textOpenapiJSONLower),
		[]byte(`{"openapi":"3.0.0","svc":"`+svc+`"}`), writeFileInput))
}

// getBody GETs the API server's path and returns its status and body.
func getBody(t *testing.T, port, path string) (code int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:"+port+path, http.NoBody)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

// serveAPI starts the API server over root on a free port and stops it at cleanup,
// retrying when a parallel test took the port first.
func serveAPI(t *testing.T, root string) string {
	t.Helper()

	// freePort can only report a port free when it asks, and tests run in parallel, so
	// another test may bind it first: a bind failure takes a fresh port.
	var (
		port string
		srv  *http.Server
		err  error
	)

	for range iterationsAPI {
		port = freePort(t)
		srv, err = docserve.New(new(docserveCfg(root, port)), new(docserveDeps(nil, nil))).
			ServeAPIDocs(t.Context())

		if err == nil || !strings.Contains(err.Error(), "address already in use") {
			break
		}
	}

	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	return port
}

// TestDocserve_GoDocs tests the pkgsite server's start.
//
// Why this test is important:
//   - pkgsite is the Go docs; a missing tool must fail with its install hint, and the
//     process must stream to the injected writers, never process stdio, so a front-end
//     with no terminal can capture or discard its output.
//
// What it tests:
//   - a RequireTool failure is returned before anything starts;
//   - otherwise pkgsite starts in Root on GoPort, writing to the injected streams, and
//     its handle is returned.
func TestDocserve_GoDocs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	sup := mocks.NewMockSupervisor(ctrl)
	root := t.TempDir()

	var out, errOut bytes.Buffer

	deps := docserveDeps(runner, sup)

	deps.Streams = types.Streams{Out: &out, Err: &errOut}

	server := docserve.New(new(docserveCfg(root, text0)), &deps)

	runner.EXPECT().RequireTool("pkgsite", "go install pkgsite").Return(assert.AnError)

	_, err := server.ServeGoDocs(t.Context())
	require.ErrorIs(t, err, assert.AnError)

	handle := mocks.NewMockProcessHandle(ctrl)

	var spec types.ProcessSpec

	runner.EXPECT().RequireTool("pkgsite", "go install pkgsite").Return(nil)
	sup.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context, sRef *types.ProcessSpec,
			_ interfaces.Probe[interfaces.ProcessHandle],
		) (interfaces.ProcessHandle, error) {
			s := *sRef

			spec = s

			return handle, nil
		})

	got, err := server.ServeGoDocs(t.Context())
	require.NoError(t, err)
	assert.Same(t, handle, got)
	assert.Equal(t, []string{"pkgsite", "-http=:16060"}, spec.Args)
	assert.Equal(t, root, spec.Dir)
	assert.Same(t, &out, spec.Stdout)
	assert.Same(t, &errOut, spec.Stderr)
}

// TestDocserve_PythonDocs tests the pdoc server's start.
//
// Why this test is important:
//   - each package is a standalone uv project documented by its src path; a package
//     missing from the argv silently vanishes from the docs.
//
// What it tests:
//   - no package under PythonRoots is CodeInvalidInput;
//   - a package with a pyproject.toml and src/<module> is passed to `uvx pdoc` on
//     PythonPort; a start failure names the process.
func TestDocserve_PythonDocs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sup := mocks.NewMockSupervisor(ctrl)
	root := t.TempDir()
	server := docserve.New(new(docserveCfg(root, text0)), new(docserveDeps(nil, sup)))

	_, err := server.ServePythonDocs(t.Context())
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput), textVLowerAlt, err)

	pkg := filepath.Join(root, textPython, "demo")
	require.NoError(
		t,
		os.MkdirAll(filepath.Join(pkg, textSrc, "demo_mod"), mkdirAllInputDocs),
	)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(pkg, textPyprojectToml), nil, writeFileInput),
	)

	var spec types.ProcessSpec

	sup.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context, sRef *types.ProcessSpec,
			_ interfaces.Probe[interfaces.ProcessHandle],
		) (interfaces.ProcessHandle, error) {
			s := *sRef

			spec = s

			return mocks.NewMockProcessHandle(ctrl), nil
		})

	_, err = server.ServePythonDocs(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"uvx", "pdoc", "--no-browser", "--host", "localhost", "--port", "16061",
		filepath.Join(pkg, textSrc, "demo_mod"),
	}, spec.Args)

	sup.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, assert.AnError)

	_, err = server.ServePythonDocs(t.Context())
	require.ErrorContains(t, err, "start uvx")
}

// TestDocserve_APIDocsSingleSpec tests the Redoc server over one spec.
//
// Why this test is important:
//   - the page loads Redoc from a CDN; an unpinned bundle with no integrity hash runs
//     whatever the CDN serves.
//
// What it tests:
//   - / is the Redoc page for the service, loading the version-pinned bundle with its
//     integrity hash; /openapi.json is the spec; any other path is 404.
func TestDocserve_APIDocsSingleSpec(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSpec(t, root, textAPI)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(root, textServices, "README.md"), nil, writeFileInput),
	)

	port := serveAPI(t, root)

	code, body := getBody(t, port, textSlash)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "<title>Api")
	assert.Contains(t, body, `spec-url="/openapi.json"`)
	assert.Contains(t, body, "/redoc/v3.0.1/bundles/redoc.standalone.js")
	assert.Contains(t, body, `integrity="sha384-`)

	code, body = getBody(t, port, textOpenapiJSON)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"svc":"api"`)

	code, _ = getBody(t, port, "/elsewhere")
	assert.Equal(t, http.StatusNotFound, code)
}

// TestDocserve_APIDocsMultipleSpecs tests the Redoc server over several specs.
//
// Why this test is important:
//   - several services share one server, so each needs its own page and spec URL and
//     the root must list them.
//
// What it tests:
//   - / links every service; /<svc> is its Redoc page and /<svc>/openapi.json its spec.
func TestDocserve_APIDocsMultipleSpecs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSpec(t, root, textAPI)
	writeSpec(t, root, "identity")

	port := serveAPI(t, root)

	_, body := getBody(t, port, textSlash)
	assert.Contains(t, body, `href="/api"`)
	assert.Contains(t, body, `href="/identity"`)

	_, body = getBody(t, port, "/identity")
	assert.Contains(t, body, `spec-url="/identity/openapi.json"`)

	_, body = getBody(t, port, "/identity/openapi.json")
	assert.Contains(t, body, `"svc":"identity"`)
}

// TestDocserve_APIDocsFailures tests what stops the Redoc server from starting.
//
// Why this test is important:
//   - an incomplete docs index with no sign of why a service is missing, or a silent
//     failure to listen, leaves the operator with a server that lies.
//
// What it tests:
//   - no ServerDir is CodeInternal; no spec is CodeNotFound; a spec that exists but
//     cannot be read is CodeInternal naming it; a busy port is CodeInternal.
func TestDocserve_APIDocsFailures(t *testing.T) {
	t.Parallel()

	serve := func(root, port string) error {
		_, err := docserve.New(new(docserveCfg(root, port)), new(docserveDeps(nil, nil))).
			ServeAPIDocs(t.Context())

		return err
	}

	err := serve(t.TempDir(), text0)
	assert.True(t, apperr.Is(err, apperr.CodeInternal), textVLowerAlt, err)

	empty := t.TempDir()
	require.NoError(
		t,
		os.MkdirAll(filepath.Join(empty, textServices, textAPI), mkdirAllInputFailures),
	)

	err = serve(empty, text0)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), textVLowerAlt, err)

	unreadable := t.TempDir()
	require.NoError(t, os.MkdirAll(
		filepath.Join(
			unreadable,
			textServices,
			textAPI,
			textDocsLower,
			textOpenapiJSONLower,
		),
		mkdirAllInputFailures,
	))

	err = serve(unreadable, text0)
	assert.True(t, apperr.Is(err, apperr.CodeInternal), textVLowerAlt, err)
	assert.Contains(
		t,
		err.Error(),
		filepath.Join(textAPI, textDocsLower, textOpenapiJSONLower),
	)

	busy := t.TempDir()
	writeSpec(t, busy, textAPI)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), textTCP, ":0")
	require.NoError(t, err)

	defer func() { _ = ln.Close() }()

	err = serve(busy, strconv.Itoa(asType[*net.TCPAddr](t, ln.Addr()).Port))
	assert.ErrorContains(t, err, "listen")
}

// TestDocserve_ServeRunsUntilCancelled tests Serve's lifecycle.
//
// Why this test is important:
//   - Serve owns the servers' lifetime: it must block while they run and shut them
//     down when the context ends, or a stopped command leaves a port bound.
//
// What it tests:
//   - missing streams are CodeInvalidInput before anything starts;
//   - an API-only Serve blocks until cancel, then returns nil with the port released;
//   - an API-only Serve whose server cannot start returns its error.
func TestDocserve_ServeRunsUntilCancelled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSpec(t, root, textAPI)

	port := freePort(t)
	cfg := docserveCfg(root, port)

	err := docserve.New(&cfg, &docserve.Deps{Log: fixtures.NopLogger()}).
		Serve(t.Context(), docserve.Targets{API: true})
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), textVLowerAlt, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		ds := docserve.New(&cfg, new(docserveDeps(nil, nil)))
		done <- ds.Serve(ctx, docserve.Targets{API: true})
	}()

	require.Eventually(t, func() bool {
		conn, opErr := (&net.Dialer{}).DialContext(
			t.Context(),
			textTCP,
			"127.0.0.1:"+port,
		)
		if opErr == nil {
			_ = conn.Close()
		}

		return opErr == nil
	}, 5*time.Second, 20*time.Millisecond)
	cancel()

	select {
	case opErr := <-done:
		require.NoError(t, opErr)
	case <-time.After(10 * time.Second):
		require.Fail(t, "Serve did not return after cancel")
	}

	err = docserve.New(new(docserveCfg(t.TempDir(), text0)), new(docserveDeps(nil, nil))).
		Serve(t.Context(), docserve.Targets{API: true})
	assert.Error(t, err)
}

const (
	// iterationsAPI is the iterations api the tests use.
	iterationsAPI = 5

	// mkdirAllInputDocs is the mkdir all input docs the tests use.
	mkdirAllInputDocs = 0o750

	// mkdirAllInputFailures is the mkdir all input failures the tests use.
	mkdirAllInputFailures = 0o750

	// mkdirAllInputSpec is the mkdir all input spec the tests use.
	mkdirAllInputSpec = 0o750
)

const (
	// textOpenapiJSONLower is the literal "openapi.json".
	textOpenapiJSONLower = "openapi.json"
)
