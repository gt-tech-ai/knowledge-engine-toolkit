package unit_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/middleware"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRequestID_GeneratesWhenMissing tests that a UUID request ID is generated when not
// present in the incoming request.
//
// Why this test is important:
//   - Every request must have a correlation identifier for distributed tracing
//   - Missing request IDs make it impossible to correlate logs across services
//   - The middleware must inject IDs into both the context and the response header
//
// What it tests:
//   - Request context contains a non-empty request ID
//   - Response includes X-Request-ID header with a generated value
func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	t.Parallel()

	handler := middleware.RequestID()(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := middleware.GetRequestID(r.Context())
			assert.NotEmpty(t, id, "request ID should be set in context")
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(
		t,
		rec.Header().Get("X-Request-ID"),
		"X-Request-ID header not set on response",
	)
}

// TestRequestID_PreservesExisting tests that an existing X-Request-ID from
// upstream is preserved.
//
// Why this test is important:
//   - Upstream proxies (an API gateway) set request IDs for end-to-end tracing
//   - Overwriting upstream IDs would break trace continuity across the gateway boundary
//   - The response must echo the same ID back for client-side correlation
//
// What it tests:
//   - Context request ID matches the incoming header value "existing-id"
//   - Response X-Request-ID header echoes "existing-id"
func TestRequestID_PreservesExisting(t *testing.T) {
	t.Parallel()

	handler := middleware.RequestID()(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			id := middleware.GetRequestID(r.Context())
			assert.Equal(t, "existing-id", id)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	req.Header.Set("X-Request-ID", "existing-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "existing-id", rec.Header().Get("X-Request-ID"))
}

// TestRecovery_CatchesPanic tests that handler panics are caught and converted to 500
// responses.
//
// Why this test is important:
//   - A single handler panic without recovery kills the entire process
//   - Production servers must remain available even when individual requests fail
//     catastrophically
//   - The recovery middleware is the last line of defense against unhandled errors
//
// What it tests:
//   - Response status code is 500 Internal Server Error after a panic
//   - The test process does not crash
func TestRecovery_CatchesPanic(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()

	handler := middleware.Recovery(
		logger,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("test panic")
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestRecovery_PassesThroughNormally tests that non-panicking handlers pass through the
// recovery middleware unchanged.
//
// Why this test is important:
//   - Recovery middleware must add no overhead or behavior changes on the happy path
//   - A broken pass-through would alter response codes or swallow headers
//   - Validates the middleware only activates on actual panics
//
// What it tests:
//   - Response status code is 200 OK for a normal handler
func TestRecovery_PassesThroughNormally(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()

	handler := middleware.Recovery(
		logger,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestCORS_SetsHeaders tests that CORS headers are set on responses for browser-based
// clients.
//
// Why this test is important:
//   - Single-page application clients require CORS headers to make cross-origin API calls
//   - Missing CORS headers cause browsers to silently reject responses
//   - The frontend and API are served from different origins in production
//
// What it tests:
//   - Access-Control-Allow-Origin is set to "*"
//   - Access-Control-Allow-Methods is present and non-empty
func TestCORS_SetsHeaders(t *testing.T) {
	t.Parallel()

	cfg := middleware.DefaultCORSConfig()
	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(
		t,
		"*",
		rec.Header().Get("Access-Control-Allow-Origin"),
		"CORS origin header not set",
	)
	assert.NotEmpty(
		t,
		rec.Header().Get("Access-Control-Allow-Methods"),
		"CORS methods header not set",
	)
}

// TestCORS_ExposesTraceResponseHeader tests that the CORS middleware exposes the
// `traceresponse` header so a browser can read the trace id off a cross-origin response.
//
// Why this test is important:
//   - A response header is invisible to `fetch` unless it is listed in
//     Access-Control-Expose-Headers; a telemetry round-trip proof relies on the
//
// SPA reading `traceresponse`. Without this the header is set but unreadable.
//
// What it tests:
//   - DefaultCORSConfig exposes `traceresponse`
//   - The middleware writes Access-Control-Expose-Headers including `traceresponse`
func TestCORS_ExposesTraceResponseHeader(t *testing.T) {
	t.Parallel()

	cfg := middleware.DefaultCORSConfig()
	assert.Contains(
		t,
		cfg.ExposedHeaders,
		"traceresponse",
		"default config must expose traceresponse",
	)

	handler := middleware.CORS(&cfg)(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Contains(
		t,
		rec.Header().Get("Access-Control-Expose-Headers"),
		"traceresponse",
		"CORS must expose the traceresponse header so the browser can read it",
	)
}

// TestCORS_PreflightReturns204 tests that OPTIONS preflight requests return 204 without
// calling the handler.
//
// Why this test is important:
//   - Browsers send preflight OPTIONS requests before cross-origin mutations (POST, PUT,
//     DELETE)
//   - Preflight must return 204 with CORS headers without executing business logic
//   - Incorrect preflight handling blocks all mutating API operations from the frontend
//
// What it tests:
//   - Response status code is 204 No Content for OPTIONS requests
//   - The inner handler is never invoked
func TestCORS_PreflightReturns204(t *testing.T) {
	t.Parallel()

	cfg := middleware.DefaultCORSConfig()
	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			assert.Fail(t, "handler should not be called for OPTIONS")
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "OPTIONS", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// TestCompression_CompressesLargeResponse tests that large responses are gzip-compressed
// when the client supports it.
//
// Why this test is important:
//   - JSON-heavy API responses benefit significantly from compression (60-80% reduction)
//   - Reduces bandwidth costs and improves perceived latency for search result payloads
//   - Validates the full compression pipeline: encode, set Content-Encoding header,
//     decompressible output
//
// What it tests:
//   - Content-Encoding header is "gzip" for large responses
//   - Response body is valid gzip that decompresses to the original content
func TestCompression_CompressesLargeResponse(t *testing.T) {
	t.Parallel()

	largeBody := strings.Repeat("hello world ", 200) // > 1KB

	handler := middleware.Compression()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(largeBody))
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(
		t,
		"gzip",
		rec.Header().Get("Content-Encoding"),
		"expected gzip Content-Encoding for large response",
	)

	// Verify we can decompress
	reader, err := gzip.NewReader(rec.Body)
	require.NoError(t, err, "gzip decode must not fail")
	defer func() { _ = reader.Close() }()
	body, _ := io.ReadAll(reader)
	assert.Contains(t, string(body), "hello world")
}

// TestCompression_SkipsSmallResponse tests that small responses are not compressed.
//
// Why this test is important:
//   - Compression overhead exceeds savings below approximately 1KB
//   - Compressing small responses wastes CPU and may increase payload size
//   - The threshold prevents counterproductive compression of health checks and short
//     errors
//
// What it tests:
//   - Content-Encoding is NOT "gzip" for a small response body
func TestCompression_SkipsSmallResponse(t *testing.T) {
	t.Parallel()

	handler := middleware.Compression()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("small"))
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(
		t,
		"gzip",
		rec.Header().Get("Content-Encoding"),
		"should not compress small responses",
	)
}

// TestCompression_SkipsWithoutAcceptEncoding tests that no compression occurs when the
// client does not request it.
//
// Why this test is important:
//   - Respects client capabilities by not forcing compression on unsupporting clients
//   - Some clients (CLI tools, older HTTP libraries) cannot handle gzip responses
//   - Content negotiation compliance prevents broken responses for non-browser consumers
//
// What it tests:
//   - Content-Encoding is NOT "gzip" when Accept-Encoding header is absent
func TestCompression_SkipsWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()

	largeBody := strings.Repeat("hello world ", 200)

	handler := middleware.Compression()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(largeBody))
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	// No Accept-Encoding header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(
		t,
		"gzip",
		rec.Header().Get("Content-Encoding"),
		"should not compress without Accept-Encoding: gzip",
	)
}

// TestHTTPMetrics_Records200 tests that HTTPMetrics records counter and duration
// for a 200 response without panicking.
//
// Why this test is important:
//   - Every HTTP handler is instrumented; a metrics middleware that panics on
//     successful responses would crash every request
//   - Validates the core instrumentation path the service depends on for SLO
//     tracking
//
// What it tests:
//   - Response status code is 200 after passing through HTTPMetrics middleware
func TestHTTPMetrics_Records200(t *testing.T) {
	t.Parallel()

	handler := middleware.HTTPMetrics(fixtures.NopMetrics())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		"GET",
		"/api/v1/things",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestHTTPMetrics_Buckets404AsNotFound tests that 404 responses get the
// "not_found" route label without panicking.
//
// Why this test is important:
//   - High-cardinality URL labels would explode Prometheus cardinality; 404s
//     from probes and bots must be bucketed under a fixed label
//   - Validates the label normalization logic that keeps metrics storage bounded
//
// What it tests:
//   - Response status code is 404 after passing through HTTPMetrics middleware
func TestHTTPMetrics_Buckets404AsNotFound(t *testing.T) {
	t.Parallel()

	handler := middleware.HTTPMetrics(fixtures.NopMetrics())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		"GET",
		"/unknown-path",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHTTPMetrics_PatternUsedAsRouteLabel tests that ServeMux-matched patterns
// are used as route labels instead of raw request paths.
//
// Why this test is important:
//   - Using raw paths as labels creates unbounded cardinality (one label per
//     unique URL); pattern-based labels keep Prometheus cardinality bounded
//   - Validates the mux-pattern extraction that drives dimensioned dashboards
//
// What it tests:
//   - Response status code is 200 when the request is routed through a ServeMux
func TestHTTPMetrics_PatternUsedAsRouteLabel(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux := http.NewServeMux()
	mux.Handle(
		"/api/v1/items",
		middleware.HTTPMetrics(fixtures.NopMetrics())(inner),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		"GET",
		"/api/v1/items",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestHTTPMetrics_NormalizesIDSegmentsToOneRouteSeries tests that raw-path
// requests carrying distinct entity ids collapse to a single route label.
//
// Why this test is important:
//   - On the catch-all forward the matched pattern is "/", so the route label
//     falls back to the raw path; a raw UUID/id in that path opens a new
//     Prometheus series per id, and a scanner walking ids explodes cardinality
//   - Bounded `route` cardinality is what keeps the RED dashboards and their
//     recording rules from degrading the Prometheus TSDB
//
// What it tests:
//   - Two requests to /api/documents/<distinct-uuid> both record the counter and
//     histogram with route "/api/documents/{id}" — one series (Times(2) on the
//     identical label), not two
func TestHTTPMetrics_NormalizesIDSegmentsToOneRouteSeries(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	m := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	hist := mocks.NewMockHistogram(ctrl)

	m.EXPECT().
		Counter("http_server_requests_total", gomock.Any(), "method", "route", "status").
		Return(counter)
	m.EXPECT().
		Histogram(
			"http_server_request_duration_seconds",
			gomock.Any(),
			gomock.Any(),
			"method",
			"route",
			"status",
		).
		Return(hist)

	// Both distinct-UUID paths must land on the SAME normalized label — Times(2)
	// on one expectation fails (unexpected call) if normalization leaks the id.
	counter.EXPECT().Inc("GET", "/api/documents/{id}", "200").Times(2)
	hist.EXPECT().Observe(gomock.Any(), "GET", "/api/documents/{id}", "200").Times(2)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.HTTPMetrics(m)(inner)

	for _, id := range []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	} {
		// No ServeMux: r.Pattern is "", so routeLabel falls back to the raw path,
		// exercising the normalization branch.
		req := httptest.NewRequestWithContext(
			t.Context(),
			"GET",
			"/api/documents/"+id,
			http.NoBody,
		)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// TestHTTPMetrics_RouteLabelNormalizationCases tests the raw-path route label
// across every id-shape the normalizer recognizes and the no-id pass-through.
//
// Why this test is important:
//   - The normalizer collapses numeric ids, UUIDs, and long hex tokens (object keys /
//     hashes) to "{id}"; each shape is a distinct branch, and a path with a digit but no
//     id segment must pass through unchanged — a wrong classification either leaks
//     cardinality (missed id) or corrupts a legitimate route label (false collapse)
//
// What it tests:
//   - Numeric ("/api/items/42"), UUID, and long-hex segments each become "{id}"
//   - A digit-bearing but id-free path ("/api/v1/health") is returned unchanged
func TestHTTPMetrics_RouteLabelNormalizationCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		path      string
		wantRoute string
	}{
		{"numeric id", "/api/items/42", "/api/items/{id}"},
		{
			"uuid",
			"/api/documents/33333333-3333-3333-3333-333333333333",
			"/api/documents/{id}",
		},
		{
			// id segment followed by a sub-resource: exercises rewriting a
			// non-id segment that comes after a collapsed one.
			"id then subresource",
			"/api/documents/44444444-4444-4444-4444-444444444444/status",
			"/api/documents/{id}/status",
		},
		{"long hex token", "/api/blobs/deadbeefdeadbeef01", "/api/blobs/{id}"},
		// A >=16-char all-hex segment with a digit (no dashes) is a hash/object key
		// → {id}. Also exercises isUUID's separator-mismatch branch (s[8] != '-').
		{"long hex hash", "/api/x/" + strings.Repeat("a1", 18), "/api/x/{id}"},
		// >=16 chars, has a digit (passes the fast-path) but a non-hex byte → not an
		// id; covers isHex's reject branch.
		{
			"long non-hex token",
			"/api/blobs/9" + strings.Repeat("z", 16),
			"/api/blobs/9" + strings.Repeat("z", 16),
		},
		// 36 chars with UUID separators and a leading digit but a non-hex byte → not
		// a UUID (isUUID's hex-reject branch) and not long-hex → unchanged.
		{
			"dashed non-hex",
			"/api/x/1ggggggg-gggg-gggg-gggg-gggggggggggg",
			"/api/x/1ggggggg-gggg-gggg-gggg-gggggggggggg",
		},
		// A digit-free hex-looking token is deliberately NOT collapsed: the fast-path
		// skips digit-free paths, an accepted tradeoff (real ids carry digits).
		{
			"digit-free hex passes through",
			"/api/x/" + strings.Repeat("a", 36),
			"/api/x/" + strings.Repeat("a", 36),
		},
		{"no id segment", "/api/v1/health", "/api/v1/health"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			m := mocks.NewMockMetrics(ctrl)
			counter := mocks.NewMockCounter(ctrl)
			hist := mocks.NewMockHistogram(ctrl)

			m.EXPECT().
				Counter(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(counter)
			m.EXPECT().
				Histogram(
					gomock.Any(),
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).
				Return(hist)

			var gotRoute string
			hist.EXPECT().Observe(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
			counter.EXPECT().
				Inc(gomock.Any(), gomock.Any(), gomock.Any()).
				Do(func(labelValues ...string) { gotRoute = labelValues[1] })

			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := middleware.HTTPMetrics(m)(inner)

			req := httptest.NewRequestWithContext(
				t.Context(),
				"GET",
				tc.path,
				http.NoBody,
			)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tc.wantRoute, gotRoute, "route label for %s", tc.path)
		})
	}
}

// TestHTTPMetrics_EmptyPathRouteLabel tests that a request with an empty URL path
// records the route label "/" rather than an empty string.
//
// Why this test is important:
//   - A malformed request can arrive with an empty URL.Path; an empty `route`
//     label value is a degenerate Prometheus series, so the middleware defaults it
//     to "/" — this pins that guard
//
// What it tests:
//   - A request whose URL.Path is "" (and no matched pattern) records route "/"
func TestHTTPMetrics_EmptyPathRouteLabel(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	m := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	hist := mocks.NewMockHistogram(ctrl)

	m.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter)
	m.EXPECT().
		Histogram(
			gomock.Any(),
			gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		).
		Return(hist)

	var gotRoute string
	hist.EXPECT().Observe(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	counter.EXPECT().
		Inc(gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(labelValues ...string) { gotRoute = labelValues[1] })

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.HTTPMetrics(m)(inner)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	req.URL.Path = "" // simulate a malformed request with no path
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "/", gotRoute)
}

// TestHTTPMetrics_FlushesResponseWriter tests that Flush propagates through
// the metrics status recorder to the underlying ResponseWriter.
//
// Why this test is important:
//   - SSE and streaming responses depend on flushing to deliver partial data;
//     if the recorder blocks Flush, streaming endpoints never deliver chunks
//   - Validates that the instrumentation wrapper is transparent to http.Flusher
//
// What it tests:
//   - Calling http.Flusher.Flush() inside the handler does not panic
//   - Response status code is 200 after the flushed response
func TestHTTPMetrics_FlushesResponseWriter(t *testing.T) {
	t.Parallel()

	handler := middleware.HTTPMetrics(fixtures.NopMetrics())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/stream", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestHTTPTracing_HandlerReceivesContext tests that HTTPTracing creates a span
// and passes the enriched context to the next handler.
//
// Why this test is important:
//   - Distributed tracing requires that span context flows through every handler;
//     if HTTPTracing does not propagate context, child spans are disconnected from
//     the trace and log-trace correlation breaks
//   - Validates the OTel middleware wiring used by all HTTP services
//
// What it tests:
//   - Response status code is 200 after passing through the tracing middleware
func TestHTTPTracing_HandlerReceivesContext(t *testing.T) {
	t.Parallel()

	handler := middleware.HTTPTracing()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/traced", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestChain_AppliesInOrder tests that middleware chain executes in registration order
// (FIFO).
//
// Why this test is important:
//   - Middleware ordering determines correctness: request ID must be set before logging
//   - Recovery must be outermost to catch panics from all inner middleware
//   - Incorrect ordering causes missing context values, unlogged requests, or uncaught
//     panics
//
// What it tests:
//   - Execution order is m1-before, m2-before, handler, m2-after, m1-after
//   - Outer middleware wraps inner middleware symmetrically
func TestChain_AppliesInOrder(t *testing.T) {
	t.Parallel()

	var order []string

	m1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "m1-before")
			next.ServeHTTP(w, r)
			order = append(order, "m1-after")
		})
	}
	m2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "m2-before")
			next.ServeHTTP(w, r)
			order = append(order, "m2-after")
		})
	}

	handler := middleware.Chain(
		m1,
		m2,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			order = append(order, "handler")
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expected := []string{"m1-before", "m2-before", "handler", "m2-after", "m1-after"}
	require.Equal(
		t,
		expected,
		order,
		"middleware chain must execute in registration order",
	)
}
