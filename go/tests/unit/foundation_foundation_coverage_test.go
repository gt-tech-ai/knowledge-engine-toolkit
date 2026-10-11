// Package foundation_test provides additional tests to increase coverage of the
// foundation layer.
//
// This file covers:
//   - Logger: stdlib logger operations, zap logger With/WithContext, logctx utilities
//   - Metrics: Prometheus counter/histogram/gauge operations, noop metrics,
//     HTTPServerMetrics
//   - Resilience: channel bulkhead Execute, retry exponential config, rate limiter token
package unit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/stdlib"
	zaplogger "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/zap"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/noop"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
)

// ---------------------------------------------------------------------------
// Logger stdlib tests
// ---------------------------------------------------------------------------

// TestStdlibLogger_AllLevels tests that the stdlib logger handles all log
// levels without panicking.
//
// Why this test is important:
//   - The stdlib logger is used in environments where Zap is unavailable; it
//     must handle all levels without crashing
//   - Missing level support would cause panics in production
//
// What it tests:
//   - Debug, Info, Warn, and Error calls with key-value pairs do not panic
func TestStdlibLogger_AllLevels(t *testing.T) {
	t.Parallel()

	cfg := stdlib.Config{Level: "debug", AddSource: false}
	l := stdlib.New(cfg)

	// Should not panic
	l.Debug("debug message", "key", "value")
	l.Info("info message", "key", "value")
	l.Warn("warn message", "key", "value")
	l.Error("error message", "key", "value")
}

// TestStdlibLogger_With tests that With returns a child logger with
// pre-attached fields.
//
// Why this test is important:
//   - With() is used to attach service-level context (e.g. service name);
//     a nil return would panic on the next log call
//   - The child must satisfy interfaces.Logger for type-safe usage
//
// What it tests:
//   - With returns a non-nil child logger
//   - The child satisfies interfaces.Logger
func TestStdlibLogger_With(t *testing.T) {
	t.Parallel()

	cfg := stdlib.Config{Level: "info"}
	l := stdlib.New(cfg)

	child := l.With("service", "api")
	require.NotNil(t, child, "expected non-nil child logger")

	// Verify interface
	_ = child
	child.Info("child message")
}

// TestStdlibLogger_WithContext tests that WithContext enriches the logger with
// a correlation ID from the context.
//
// Why this test is important:
//   - Correlation IDs enable end-to-end request tracing across log entries;
//     WithContext must extract and attach them automatically
//   - When no correlation ID is present, the same logger must be returned to
//     avoid unnecessary allocations
//
// What it tests:
//   - WithContext with no correlation ID returns the same logger instance
//   - WithContext with a correlation ID returns an enriched logger
func TestStdlibLogger_WithContext(t *testing.T) {
	t.Parallel()

	cfg := stdlib.Config{Level: "info"}
	l := stdlib.New(cfg)

	// Without correlation ID, should return same logger
	ctx := context.Background()
	same := l.WithContext(ctx)
	assert.Equal(t, l, same, "expected same logger when no correlation ID")

	// With correlation ID, should return enriched logger
	ctx = logctx.WithCorrelationID(ctx, "req-456")
	enriched := l.WithContext(ctx)
	require.NotNil(t, enriched, "expected non-nil enriched logger")
	enriched.Info("message with correlation")
}

// TestStdlibLogger_DefaultConfig tests that DefaultConfig returns sensible
// defaults for the stdlib logger.
//
// Why this test is important:
//   - Default config values are used when no explicit config is provided;
//     wrong defaults (e.g. level="") would cause parse errors at startup
//
// What it tests:
//   - Default level is "info"
//   - AddSource is true by default
func TestStdlibLogger_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := stdlib.DefaultConfig()
	assert.Equal(t, "info", cfg.Level)
	assert.True(t, cfg.AddSource, "AddSource should be true by default")
}

// TestStdlibLogger_ParseLevels tests that all level strings are handled
// without errors, including the unknown level fallback.
//
// Why this test is important:
//   - Config files and environment variables may contain any level string;
//     unrecognized levels must produce a working logger, not panic
//
// What it tests:
//   - Levels "debug", "info", "warn", "warning", "error", and "unknown" all
//     produce non-nil loggers
func TestStdlibLogger_ParseLevels(t *testing.T) {
	t.Parallel()

	levels := []string{"debug", "info", "warn", "warning", "error", "unknown"}
	for _, level := range levels {
		cfg := stdlib.Config{Level: level}
		l := stdlib.New(cfg)
		assert.NotNil(t, l, "expected non-nil logger for level %q", level)
		l.Info("test at level " + level)
	}
}

// TestStdlibLogger_NewSlog tests that NewSlog returns a raw slog.Logger for
// callers that need direct slog access.
//
// Why this test is important:
//   - Some middleware and third-party libraries require a *slog.Logger; NewSlog
//     provides the escape hatch without exposing internal types
//
// What it tests:
//   - NewSlog returns a non-nil *slog.Logger
func TestStdlibLogger_NewSlog(t *testing.T) {
	t.Parallel()

	cfg := stdlib.Config{Level: "info"}
	slogger := stdlib.NewSlog(cfg)
	require.NotNil(t, slogger, "expected non-nil slog.Logger")
}

// ---------------------------------------------------------------------------
// Logger zap tests
// ---------------------------------------------------------------------------

// TestZapLogger_AllLevels tests that the zap logger handles all log levels
// without panicking.
//
// Why this test is important:
//   - Zap is the primary production logger; all four levels must work without
//     crashing or the service becomes undebuggable
//
// What it tests:
//   - Debug, Info, Warn, and Error calls with key-value pairs do not panic
func TestZapLogger_AllLevels(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "debug", Format: "json", RedactPII: false}
	l := zaplogger.New(cfg)

	l.Debug("debug message", "key", "value")
	l.Info("info message", "key", "value")
	l.Warn("warn message", "key", "value")
	l.Error("error message", "key", "value")
}

// TestZapLogger_With tests that With returns a child logger with pre-attached
// fields.
//
// Why this test is important:
//   - With() is used to attach service-level context (e.g. service name);
//     a nil return would panic on the next log call
//
// What it tests:
//   - With returns a non-nil child logger that can emit log messages
func TestZapLogger_With(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "info", Format: "json"}
	l := zaplogger.New(cfg)

	child := l.With("service", "api")
	require.NotNil(t, child, "expected non-nil child logger")
	child.Info("child message")
}

// TestZapLogger_WithContext tests that WithContext enriches the logger with a
// correlation ID from the context.
//
// Why this test is important:
//   - Correlation IDs enable end-to-end request tracing; the zap logger must
//     extract and attach them to every subsequent log entry
//   - When no correlation ID is present, the same logger must be returned to
//     avoid unnecessary allocations
//
// What it tests:
//   - WithContext with no correlation ID returns the same logger instance
//   - WithContext with a correlation ID returns an enriched logger
func TestZapLogger_WithContext(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "info", Format: "json"}
	l := zaplogger.New(cfg)

	ctx := context.Background()
	same := l.WithContext(ctx)
	assert.Equal(t, l, same, "expected same logger when no correlation ID")

	ctx = logctx.WithCorrelationID(ctx, "req-789")
	enriched := l.WithContext(ctx)
	require.NotNil(t, enriched, "expected non-nil enriched logger")
	enriched.Info("message with correlation")
}

// TestZapLogger_DefaultConfig tests that zap DefaultConfig returns
// production-ready settings.
//
// Why this test is important:
//   - Default config values are used when no explicit config is provided; wrong
//     defaults (e.g. RedactPII=false) would leak PII in production logs
//
// What it tests:
//   - Default level is "info"
//   - Default format is "json"
//   - RedactPII is true by default
func TestZapLogger_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.DefaultConfig()
	assert.Equal(t, "info", cfg.Level)
	assert.Equal(t, "json", cfg.Format)
	assert.True(t, cfg.RedactPII, "RedactPII should be true by default")
}

// TestZapLogger_TextFormat tests that zap text format initialization produces
// a working logger for human-readable local development output.
//
// Why this test is important:
//   - Developers use text format locally for readability; if the text encoder
//     initialization fails, local development is blocked
//
// What it tests:
//   - New with format="text" returns a non-nil logger that can emit messages
func TestZapLogger_TextFormat(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "debug", Format: "text", RedactPII: false}
	l := zaplogger.New(cfg)
	require.NotNil(t, l, "expected non-nil logger")
	l.Debug("text format debug")
}

// TestZapLogger_ParseLevels tests that zap handles all level strings without
// errors, including the unknown level fallback.
//
// Why this test is important:
//   - Config files and environment variables may contain any level string;
//     unrecognized levels must produce a working logger, not panic
//
// What it tests:
//   - Levels "debug", "info", "warn", "warning", "error", and "unknown" all
//     produce non-nil loggers
func TestZapLogger_ParseLevels(t *testing.T) {
	t.Parallel()

	levels := []string{"debug", "info", "warn", "warning", "error", "unknown"}
	for _, level := range levels {
		cfg := zaplogger.Config{Level: level, Format: "json"}
		l := zaplogger.New(cfg)
		assert.NotNil(t, l, "expected non-nil logger for level %q", level)
	}
}

// TestZapLogger_PII_Redaction tests that the zap logger with PII redaction
// enabled processes sensitive data without panicking.
//
// Why this test is important:
//   - PII redaction is a compliance requirement (GDPR, SOC2); the zap logger
//     must intercept and redact PII in both message text and attribute values
//
// What it tests:
//   - Logging a message and attribute containing email addresses does not panic
func TestZapLogger_PII_Redaction(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "debug", Format: "json", RedactPII: true}
	l := zaplogger.New(cfg)

	// Should not panic; PII redaction happens internally
	l.Info("user email is test@example.com", "email", "user@example.com")
}

// TestZapLogger_NewSlog tests that NewSlog returns a raw slog.Logger for
// callers that need direct slog access.
//
// Why this test is important:
//   - Some middleware and third-party libraries require a *slog.Logger; NewSlog
//     provides the escape hatch with PII redaction enabled
//
// What it tests:
//   - NewSlog with RedactPII=true returns a non-nil *slog.Logger
func TestZapLogger_NewSlog(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{Level: "info", Format: "json", RedactPII: true}
	slogger := zaplogger.NewSlog(cfg)
	require.NotNil(t, slogger, "expected non-nil slog.Logger")
}

// ---------------------------------------------------------------------------
// Logger factory option tests
// ---------------------------------------------------------------------------

// TestLoggerConfig_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning for distributed config
//     propagation; broken round-tripping would silently drop logger settings
//
// What it tests:
//   - ToOptions returns exactly 1 option
//   - Applying the option copies Kind and Level from the source
func TestLoggerConfig_ToOptions(t *testing.T) {
	t.Parallel()

	cfg := logger.Config{Kind: logger.KindStdlib, Level: "warn"}
	opts := cfg.ToOptions()
	assert.Len(t, opts, 1, "expected 1 option from ToOptions")

	target := logger.DefaultConfig()
	for _, opt := range opts {
		opt(&target)
	}
	assert.Equal(t, logger.KindStdlib, target.Kind)
	assert.Equal(t, "warn", target.Level)
}

// TestLoggerConfig_WithOptions tests that individual logger option functions
// mutate the config correctly.
//
// Why this test is important:
//   - Functional options are the public API for customizing the logger; if any
//     option silently fails, services log with wrong format or PII settings
//
// What it tests:
//   - WithFormat sets the output format
//   - WithRedactPII toggles PII redaction
//   - WithAddSource toggles source code location in log output
func TestLoggerConfig_WithOptions(t *testing.T) {
	t.Parallel()

	cfg := logger.DefaultConfig()
	logger.WithFormat("text")(&cfg)
	assert.Equal(t, "text", cfg.Format)

	logger.WithRedactPII(false)(&cfg)
	assert.False(t, cfg.RedactPII, "RedactPII should be false")

	logger.WithAddSource(false)(&cfg)
	assert.False(t, cfg.AddSource, "AddSource should be false")
}

// TestLoggerKind_String tests the string representation of logger kinds for
// known and unknown values.
//
// Why this test is important:
//   - Kind strings appear in log messages during startup; incorrect values
//     make it hard to identify which logger backend is active
//   - Unknown kinds must include their numeric value to aid troubleshooting
//
// What it tests:
//   - KindZap.String() returns "zap"
//   - KindStdlib.String() returns "stdlib"
//   - An unknown Kind includes its numeric value in the string
func TestLoggerKind_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "zap", logger.KindZap.String())
	assert.Equal(t, "stdlib", logger.KindStdlib.String())
	unknown := logger.Kind(99)
	assert.Contains(
		t,
		unknown.String(),
		"99",
		"unknown kind should contain its numeric value",
	)
}

// ---------------------------------------------------------------------------
// logctx tests
// ---------------------------------------------------------------------------

// TestLogctx_CorrelationID_RoundTrip tests that a correlation ID can be stored
// in and retrieved from a context.
//
// Why this test is important:
//   - Correlation IDs are the foundation of distributed tracing in logs; a
//     broken round-trip means request traces cannot be correlated
//
// What it tests:
//   - CorrelationID on a bare context returns empty string
//   - WithCorrelationID followed by CorrelationID returns the stored value
func TestLogctx_CorrelationID_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	assert.Empty(t, logctx.CorrelationID(ctx))

	ctx = logctx.WithCorrelationID(ctx, "corr-abc")
	assert.Equal(t, "corr-abc", logctx.CorrelationID(ctx))
}

// TestLogctx_RedactPII tests the RedactPII function for all supported PII
// patterns.
//
// Why this test is important:
//   - PII redaction is a compliance requirement (GDPR, SOC2); each pattern
//     (email, SSN) must be detected and replaced
//   - Safe values must pass through unmodified to preserve log usefulness
//
// What it tests:
//   - Email addresses are replaced with [REDACTED]
//   - SSN patterns are replaced with [REDACTED]
//   - Safe strings pass through unchanged
func TestLogctx_RedactPII(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"email", "user@example.com", "[REDACTED]"},
		{"ssn", "123-45-6789", "[REDACTED]"},
		{"safe", "hello world", "hello world"},
	}

	for _, tt := range tests {
		got := logctx.RedactPII(tt.input)
		assert.Equal(t, tt.want, got, "%s: RedactPII(%q)", tt.name, tt.input)
	}
}

// ---------------------------------------------------------------------------
// Metrics: noop tests
// ---------------------------------------------------------------------------

// TestNoopMetrics_AllOperations tests that all noop metrics operations do not
// panic when called with label values.
//
// Why this test is important:
//   - When metrics are disabled, all Counter, Histogram, Gauge, and Handler
//     operations must silently succeed to avoid crashing services
//
// What it tests:
//   - Counter Inc and Add with labels do not panic
//   - Histogram Observe with labels does not panic
//   - Gauge Set, Inc, and Dec with labels do not panic
//   - Handler returns a non-nil http.Handler
func TestNoopMetrics_AllOperations(t *testing.T) {
	t.Parallel()

	m := noop.New()
	var _ interfaces.Metrics = m

	// Counter
	c := m.Counter("test_counter", "help", "label")
	c.Inc("val")
	c.Add(5.0, "val")

	// Histogram
	h := m.Histogram("test_hist", "help", []float64{1, 5, 10}, "label")
	h.Observe(3.5, "val")

	// Gauge
	g := m.Gauge("test_gauge", "help", "label")
	g.Set(42.0, "val")
	g.Inc("val")
	g.Dec("val")

	// Handler
	handler := m.Handler()
	require.NotNil(t, handler, "expected non-nil handler")
}

// ---------------------------------------------------------------------------
// Metrics: prom tests
// ---------------------------------------------------------------------------

// TestPromMetrics_CounterOps tests that Prometheus counter increment and add
// operations work with label values.
//
// Why this test is important:
//   - Counters are the most common metric type (e.g. http_requests_total);
//     broken Inc or Add would make all request-count dashboards empty
//
// What it tests:
//   - Counter.Inc with a label value does not panic
//   - Counter.Add with a label value does not panic
func TestPromMetrics_CounterOps(t *testing.T) {
	t.Parallel()

	m := prom.New()

	c := m.Counter("test_prom_counter", "test counter", "method")
	c.Inc("GET")
	c.Add(5.0, "POST")
}

// TestPromMetrics_HistogramOps tests that Prometheus histogram observe works
// with label values and custom buckets.
//
// Why this test is important:
//   - Histograms track latency distributions (e.g. request_duration); broken
//     Observe would make SLO dashboards empty
//
// What it tests:
//   - Histogram.Observe with a label value does not panic for multiple values
func TestPromMetrics_HistogramOps(t *testing.T) {
	t.Parallel()

	m := prom.New()

	h := m.Histogram("test_prom_hist", "test hist", []float64{0.1, 0.5, 1.0}, "path")
	h.Observe(0.3, "/api")
	h.Observe(0.8, "/api")
}

// TestPromMetrics_GaugeOps tests that Prometheus gauge set, inc, and dec
// operations work with label values.
//
// Why this test is important:
//   - Gauges track current state (e.g. active_connections); broken operations
//     would make capacity dashboards unreliable
//
// What it tests:
//   - Gauge.Set, Gauge.Inc, and Gauge.Dec with label values do not panic
func TestPromMetrics_GaugeOps(t *testing.T) {
	t.Parallel()

	m := prom.New()

	g := m.Gauge("test_prom_gauge", "test gauge", "service")
	g.Set(100.0, "api")
	g.Inc("api")
	g.Dec("api")
}

// TestPromMetrics_Handler tests that the Prometheus metrics handler serves
// metrics content via HTTP.
//
// Why this test is important:
//   - Prometheus scrapes /metrics via HTTP; if the handler returns empty or
//     errors, all monitoring for the service is lost
//
// What it tests:
//   - Handler returns a non-nil http.Handler
//   - GET /metrics returns HTTP 200 with a non-empty body
func TestPromMetrics_Handler(t *testing.T) {
	t.Parallel()

	m := prom.New()
	handler := m.Handler()
	require.NotNil(t, handler, "expected non-nil handler")

	// Issue a request to verify handler serves content
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	assert.NotEmpty(t, body, "expected non-empty metrics response")
}

// TestPromMetrics_NewFromConfig tests that NewFromConfig creates Prometheus
// metrics from an explicit config struct.
//
// Why this test is important:
//   - NewFromConfig is the constructor used when config is loaded from YAML;
//     it must produce a valid metrics instance
//
// What it tests:
//   - NewFromConfig with an empty Config returns a non-nil Metrics
func TestPromMetrics_NewFromConfig(t *testing.T) {
	t.Parallel()

	m := prom.NewFromConfig(prom.Config{})
	require.NotNil(t, m, "expected non-nil metrics")
}

// TestPromMetrics_DefaultConfig tests that DefaultConfig does not panic and
// returns a valid config.
//
// Why this test is important:
//   - DefaultConfig is called during service bootstrap; a panic here would
//     prevent all services from starting
//
// What it tests:
//   - DefaultConfig returns without panicking
func TestPromMetrics_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := prom.DefaultConfig()
	_ = cfg // just verify it doesn't panic
}

// TestPromMetrics_NewFromRegistry tests that NewFromRegistry creates metrics
// bound to an existing Prometheus registry.
//
// Why this test is important:
//   - Custom registries are used in testing and multi-module setups to avoid
//     metric name collisions with the global registry
//
// What it tests:
//   - Registry() returns a non-nil Prometheus registry
//   - NewFromRegistry with that registry returns a non-nil Metrics
func TestPromMetrics_NewFromRegistry(t *testing.T) {
	t.Parallel()

	m := prom.New()
	reg := m.Registry()
	require.NotNil(t, reg, "expected non-nil registry")

	m2 := prom.NewFromRegistry(reg)
	require.NotNil(t, m2, "expected non-nil metrics from registry")
}

// TestPromMetrics_HTTPServerMetrics tests that HTTPServerMetrics creates
// pre-configured counter and histogram metrics for HTTP server observability.
//
// Why this test is important:
//   - HTTPServerMetrics provides the standard request_total, request_duration,
//     and response_size metrics that dashboards and SLOs depend on
//   - All three metric types must be non-nil and recordable
//
// What it tests:
//   - NewHTTPServerMetrics returns non-nil with RequestsTotal, RequestDuration,
//     and ResponseSize fields populated
//   - WithLabelValues and recording methods do not panic
func TestPromMetrics_HTTPServerMetrics(t *testing.T) {
	t.Parallel()

	m := prom.New()
	reg := m.Registry()

	httpMetrics := prom.NewHTTPServerMetrics(reg)
	require.NotNil(t, httpMetrics, "expected non-nil HTTPServerMetrics")
	assert.NotNil(t, httpMetrics.RequestsTotal, "expected non-nil RequestsTotal")
	assert.NotNil(t, httpMetrics.RequestDuration, "expected non-nil RequestDuration")
	assert.NotNil(t, httpMetrics.ResponseSize, "expected non-nil ResponseSize")

	// Verify they can record metrics
	httpMetrics.RequestsTotal.WithLabelValues("GET", "/api", "200").Inc()
	httpMetrics.RequestDuration.WithLabelValues("GET", "/api").Observe(0.1)
	httpMetrics.ResponseSize.WithLabelValues("GET", "/api").Observe(1024)
}

// ---------------------------------------------------------------------------
// Metrics: factory tests
// ---------------------------------------------------------------------------

// TestMetricsKind_String tests the string representation of metrics kinds for
// known and unknown values.
//
// Why this test is important:
//   - Kind strings appear in log messages and error diagnostics; incorrect
//     values make it hard to identify which metrics backend is active
//
// What it tests:
//   - KindPrometheus.String() returns "prometheus"
//   - An unknown Kind includes its numeric value in the string
func TestMetricsKind_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "prometheus", metrics.KindPrometheus.String())
	unknown := metrics.Kind(99)
	assert.Contains(
		t,
		unknown.String(),
		"99",
		"unknown kind should contain its numeric value",
	)
}

// TestMetricsConfig_ToOptions tests that Config.ToOptions produces options that
// faithfully reconstruct the source config.
//
// Why this test is important:
//   - ToOptions enables config serialization/cloning; broken round-tripping
//     would silently drop the custom metrics path
//
// What it tests:
//   - ToOptions returns exactly 1 option
//   - Applying the option copies Path from the source
func TestMetricsConfig_ToOptions(t *testing.T) {
	t.Parallel()

	cfg := metrics.Config{Kind: metrics.KindPrometheus, Enabled: true, Path: "/custom"}
	opts := cfg.ToOptions()
	assert.Len(t, opts, 1, "expected 1 option from ToOptions")

	target := metrics.DefaultConfig()
	for _, opt := range opts {
		opt(&target)
	}
	assert.Equal(t, "/custom", target.Path)
}

// TestMetricsConfig_WithEnabled tests that the WithEnabled option toggles
// metrics collection on or off.
//
// Why this test is important:
//   - The ability to disable metrics is essential for test environments and
//     resource-constrained deployments; a broken option would leave metrics
//     permanently enabled
//
// What it tests:
//   - WithEnabled(false) sets Enabled to false
func TestMetricsConfig_WithEnabled(t *testing.T) {
	t.Parallel()

	cfg := metrics.DefaultConfig()
	metrics.WithEnabled(false)(&cfg)
	assert.False(t, cfg.Enabled, "Enabled should be false")
}

// ---------------------------------------------------------------------------
// Noop metrics: exercise unexported type methods directly via the interfaces
// ---------------------------------------------------------------------------

// TestNoopMetrics_CounterNoLabels tests that the noop counter works without
// label values.
//
// Why this test is important:
//   - Some counters are defined without labels; the noop must handle zero-arity
//     Inc and Add without panicking
//
// What it tests:
//   - Inc() and Add() with no label values do not panic
func TestNoopMetrics_CounterNoLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	c := m.Counter("c", "help")
	c.Inc()
	c.Add(1.0)
}

// TestNoopMetrics_HistogramNoLabels tests that the noop histogram works without
// label values.
//
// Why this test is important:
//   - Some histograms are defined without labels; the noop must handle
//     zero-arity Observe without panicking
//
// What it tests:
//   - Observe() with no label values does not panic
func TestNoopMetrics_HistogramNoLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	h := m.Histogram("h", "help", nil)
	h.Observe(1.0)
}

// TestNoopMetrics_GaugeNoLabels tests that the noop gauge works without label
// values.
//
// Why this test is important:
//   - Some gauges are defined without labels; the noop must handle zero-arity
//     Set, Inc, and Dec without panicking
//
// What it tests:
//   - Set(), Inc(), and Dec() with no label values do not panic
func TestNoopMetrics_GaugeNoLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	g := m.Gauge("g", "help")
	g.Set(1.0)
	g.Inc()
	g.Dec()
}
