package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/helpers"
)

// httpDurationBuckets returns the default histogram bucket boundaries for HTTP
// request durations (seconds). Matches Prometheus DefBuckets.
func httpDurationBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	// ResponseWriter is the wrapped writer; all writes pass through to it.
	http.ResponseWriter

	// status is the captured HTTP status code, defaulting to 200 until
	// WriteHeader is called.
	status int
}

// WriteHeader records the status code before delegating to the underlying
// writer, so the middleware can label metrics and logs with the response status.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher when the underlying writer supports it (needed
// for streaming/SSE and h2c).
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HTTPMetrics returns middleware that records RED-style HTTP server metrics on
// the shared metrics registry, using the OpenTelemetry-aligned metric names the
// Grafana dashboards query:
//
//   - http_server_requests_total{method,route,status}
//   - http_server_request_duration_seconds{method,route,status}
//
// route is supplied by the caller (typically a low-cardinality matched pattern)
// to avoid unbounded label cardinality from raw request paths.
//
// This middleware records metrics only. Per-request logging is emitted once at
// the Connect logging interceptor (for RPCs), so a request is not logged twice;
// health probes are not Connect RPCs and are therefore not logged at all.
func HTTPMetrics(m interfaces.Metrics) Middleware {
	requests := m.Counter(
		"http_server_requests_total",
		"Total number of HTTP server requests",
		"method", "route", "status",
	)
	duration := m.Histogram(
		"http_server_request_duration_seconds",
		"HTTP server request duration in seconds",
		httpDurationBuckets(),
		"method", "route", "status",
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			// Bucket unmatched (404) requests so scanners hitting random URLs can't
			// explode the route label's cardinality (the catch-all forwards every
			// path here, including bogus ones). Skip route derivation entirely for a
			// 404 — the actual path is still in the log.
			route := "not_found"
			if rec.status != http.StatusNotFound {
				route = routeLabel(r)
			}
			elapsed := time.Since(start)
			status := strconv.Itoa(rec.status)
			duration.Observe(elapsed.Seconds(), r.Method, route, status)
			requests.Inc(r.Method, route, status)
		})
	}
}

// routeLabel derives the route label for the metric, log, and trace span. It uses
// the matched net/http pattern (Go 1.22+ ServeMux) when that is a specific route.
// The services register a catch-all `mux.Handle("/", ...)` that forwards to the
// Connect/JSON transcoder, so Pattern == "/" means "matched the catch-all", NOT a
// request to root: fall through to the request path (the RPC method / REST route)
// for a meaningful label. (Before ServeMux routing runs -- e.g. in the tracing
// middleware -- Pattern is empty, so this returns the path then too.) The metrics
// middleware additionally buckets 404s so scanners can't explode label cardinality.
func routeLabel(r *http.Request) string {
	if p := r.Pattern; p != "" && p != "/" {
		// A matched net/http pattern is already a low-cardinality template
		// (e.g. "/api/documents/{id}"), so it needs no normalization.
		return p
	}
	if r.URL.Path == "" {
		return "/"
	}
	// Catch-all forward (Pattern "/" or empty): the raw path carries entity ids,
	// so collapse id-shaped segments to {id} before using it as a label.
	return normalizeRoute(r.URL.Path)
}

// normalizeRoute collapses high-cardinality identifier segments in a raw request
// path to the literal "{id}" so the metrics `route` label stays bounded.
// Without this, "/api/documents/<uuidA>" and "/api/documents/<uuidB>" would open
// two Prometheus series (and a scanner walking ids would open unbounded series);
// after it, both map to "/api/documents/{id}" — one series.
//
// It only rewrites the raw-path fallback (matched patterns are already templates)
// and allocates nothing on the common paths: a segment can only be an id if the
// path contains a digit, so a digit-free path returns early, and a path that has a
// digit but no id-shaped segment (e.g. the "v1" in "/api/v1/x" or a Connect RPC
// path) is returned as-is — the rewritten string is built (via strings.Builder)
// only when a segment is actually collapsed, rather than always splitting into a
// []string. This matters because routeLabel runs on every request in both the
// metrics and tracing middlewares.
func normalizeRoute(path string) string {
	if !strings.ContainsAny(path, "0123456789") {
		return path
	}
	// Walk the '/'-separated segments in place; only start building a new string
	// once a segment is found that must be collapsed.
	var b strings.Builder
	rewritten := false
	start := 0
	for i := 0; i <= len(path); i++ {
		if i < len(path) && path[i] != '/' {
			continue
		}
		seg := path[start:i]
		switch {
		case looksLikeID(seg):
			if !rewritten {
				b.Grow(len(path) + len("{id}"))
				b.WriteString(path[:start])
				rewritten = true
			}
			b.WriteString("{id}")
		case rewritten:
			b.WriteString(seg)
		}
		if rewritten && i < len(path) {
			b.WriteByte('/')
		}
		start = i + 1
	}
	if !rewritten {
		return path
	}
	return b.String()
}

// looksLikeID reports whether a path segment is a high-cardinality identifier that
// must be collapsed to "{id}" in the route label: an all-digits numeric id, a
// canonical UUID, or a long (>=16) hex token (object keys / hashes). Each of these
// contains a digit, matching normalizeRoute's fast-path guard. The primitive
// classifiers live in the shared go/helpers leaf utility.
func looksLikeID(seg string) bool {
	switch {
	case seg == "":
		return false
	case helpers.IsAllDigits(seg):
		return true
	case helpers.IsUUID(seg):
		return true
	case len(seg) >= 16 && helpers.IsHex(seg):
		return true
	default:
		return false
	}
}
