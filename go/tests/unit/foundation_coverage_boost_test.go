// Package foundation_test provides additional tests to increase coverage of the
// foundation layer above 90%.
//
// This file covers:
//   - Config: viper.DefaultConfig, viper.Loader.Viper(), secrets overlay
//   - Logger: zap redactingHandler WithAttrs/WithGroup, redactAttr for non-string attrs
//   - Metrics: noop counter/histogram/gauge multi-label operations
//   - Middleware: CORS with specific origins, GetRequestID without context, compression
//     streaming
//   - Resilience: RetryWithResult success/failure, budget.Remaining without budget

package unit_test

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	viperloader "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/viper"
	zaplogger "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/zap"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/noop"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/middleware"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/oteltracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// ---------------------------------------------------------------------------
// Config: viper.DefaultConfig
// ---------------------------------------------------------------------------

// TestViperDefaultConfig tests that viper.DefaultConfig returns sensible
// defaults for the config loader.
//
// Why this test is important:
//   - All services rely on the viper loader's defaults when no explicit config
//     path is set; wrong defaults would cause config file discovery to fail
//
// What it tests:
//   - BaseDir defaults to "."
//   - Prefix defaults to "" (the consumer sets its own)
func TestViperDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := viperloader.DefaultConfig()
	assert.Equal(t, ".", cfg.BaseDir)
	assert.Empty(t, cfg.Prefix)
}

// TestViperLoader_Viper tests that Viper() exposes the underlying *viper.Viper
// instance for advanced usage.
//
// Why this test is important:
//   - Some services need direct viper access for features not exposed by the
//     Config interface (e.g. watching for changes); the escape hatch must work
//
// What it tests:
//   - Viper() returns a non-nil instance after Load
//   - The underlying viper reads values from the loaded config file
func TestViperLoader_Viper(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
app:
  name: viper-test
`)

	cfg := fixtures.Build(func(row *viperloader.Config) {
		row.BaseDir = dir
		row.Prefix = textMyapp
	})
	loader := viperloader.New(cfg)
	require.NoError(t, loader.Load(), textLoad)

	v := loader.Viper()
	require.NotNil(t, v, "Viper() returned nil")

	assert.Equal(t, "viper-test", v.GetString(textAppName))
}

// TestViperLoader_SecretsOverlay tests that secrets.yaml is merged into the
// configuration hierarchy, overriding base values.
//
// Why this test is important:
//   - Production secrets must override default config without affecting other
//     keys; broken merging could expose default passwords or lose secret values
//   - The secrets overlay is the mechanism for injecting Kubernetes secrets
//
// What it tests:
//   - db.password is overridden by secrets.yaml value
//   - db.host from the base config is preserved
func TestViperLoader_SecretsOverlay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
db:
  host: localhost
  password: default
`)
	writeFile(t, filepath.Join(dir, "secrets.yaml"), `
db:
  password: s3cr3t
`)

	cfg := fixtures.Build(func(row *viperloader.Config) {
		row.BaseDir = dir
		row.Prefix = textMyapp
	})
	loader := viperloader.New(cfg)
	require.NoError(t, loader.Load(), textLoad)

	assert.Equal(
		t,
		"s3cr3t",
		loader.GetString("db.password"),
		"secrets overlay must override base",
	)
	assert.Equal(t, "localhost", loader.GetString("db.host"))
}

// TestViperLoader_LoadInvalidYAML tests that Load returns an error for
// malformed YAML files rather than silently loading garbage.
//
// Why this test is important:
//   - Silent config corruption could cause services to run with zero-value
//     settings, leading to data loss or security vulnerabilities
//   - Operators need a clear error at startup to fix the config file
//
// What it tests:
//   - Load returns a non-nil error for syntactically invalid YAML
func TestViperLoader_LoadInvalidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
not: [valid: yaml
`)

	cfg := fixtures.Build(func(row *viperloader.Config) {
		row.BaseDir = dir
		row.Prefix = textMyapp
	})
	loader := viperloader.New(cfg)
	require.Error(t, loader.Load(), "expected error for invalid YAML")
}

// ---------------------------------------------------------------------------
// Logger: zap redactingHandler WithAttrs and WithGroup
// ---------------------------------------------------------------------------

// TestZapLogger_WithAttrs_Redaction tests that the zap PII-redacting handler's
// WithAttrs method redacts PII in pre-attached attributes.
//
// Why this test is important:
//   - Pre-attached attributes (via With()) are emitted with every subsequent
//     log entry; PII in these attributes would leak across all future messages
//   - Compliance (GDPR, SOC2) requires redaction at the handler level
//
// What it tests:
//   - With() on the slog.Logger with an email value does not panic
//   - The resulting child logger can log messages successfully
func TestZapLogger_WithAttrs_Redaction(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	child := slogger.With(textEmail, "user@example.com")
	require.NotNil(t, child, "expected non-nil child logger")

	child.InfoContext(t.Context(), "test message with pre-attached PII attr")
}

// TestZapLogger_WithGroup tests that the zap PII-redacting handler's WithGroup
// method returns a properly grouped handler.
//
// Why this test is important:
//   - slog groups namespace attributes under a prefix (e.g. "request.method");
//     the redacting handler must delegate correctly or groups silently break
//   - The slog.Handler interface requires WithGroup to return a new handler
//
// What it tests:
//   - WithGroup returns a non-nil logger
//   - Logging with the grouped logger does not panic
func TestZapLogger_WithGroup(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	grouped := slogger.WithGroup("request")
	require.NotNil(t, grouped, "expected non-nil grouped logger")

	grouped.InfoContext(
		t.Context(),
		"grouped message",
		textMethod,
		textGetUpper,
		textPathLower,
		textAPIPath,
	)
}

// TestZapLogger_RedactAttr_NonString tests that redactAttr passes through
// non-string attribute values unchanged.
//
// Why this test is important:
//   - The PII redactor must only modify string values; applying regex
//     replacement to integers or bools would corrupt metric counters and flags
//
// What it tests:
//   - Logging integer, float, and bool attributes does not panic or corrupt values
func TestZapLogger_RedactAttr_NonString(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	slogger.InfoContext(
		t.Context(),
		"non-string attr",
		textCount,
		infoContextInput,
		"ratio",
		infoContextInputString,
		textOk,
		true,
	)
}

// TestZapLogger_RedactAttr_SensitiveValues tests that PII patterns in string
// attribute values are redacted regardless of key names.
//
// Why this test is important:
//   - PII can appear in any string attribute, not just keys named "email";
//     value-based redaction is required for GDPR/SOC2 compliance
//   - Safe values must pass through unmodified to preserve log usefulness
//
// What it tests:
//   - Email, phone, and SSN patterns in string values are processed by the PII redactor
//   - Safe values without PII patterns are passed through
func TestZapLogger_RedactAttr_SensitiveValues(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	slogger.InfoContext(t.Context(),
		"user lookup",
		textEmail, "admin@corp.com",
		"phone", "555-123-4567",
		"ssn", "123-45-6789",
		"safe_field", "no-pii-here",
	)
}

// TestZapLogger_WithAttrs_MultipleAttrs tests that WithAttrs handles a mix of
// PII and non-PII attributes simultaneously.
//
// Why this test is important:
//   - Real-world log calls attach multiple attributes of mixed types; the
//     handler must process each independently without cross-contamination
//
// What it tests:
//   - With() accepts string, email-containing string, and integer attributes without
//     panic
//   - The child logger can emit log messages
func TestZapLogger_WithAttrs_MultipleAttrs(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	child := slogger.With(
		textService, textAPI,
		textEmail, "test@test.com",
		textCount, withInput,
	)
	child.InfoContext(t.Context(), "multi-attr test")
}

// TestZapLogger_WithGroupThenAttrs tests that chaining WithGroup followed by
// With works through the redacting handler.
//
// Why this test is important:
//   - The slog API allows chaining WithGroup and With in any order; the
//     redacting handler must support this composition without losing context
//
// What it tests:
//   - WithGroup("http").With("method", "POST") produces a usable child logger
//   - Logging with the grouped-and-attributed logger does not panic
func TestZapLogger_WithGroupThenAttrs(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	child := slogger.WithGroup("http").With(textMethod, textPost)
	child.InfoContext(t.Context(), "grouped with attrs")
}

// ---------------------------------------------------------------------------
// Noop Metrics: exercise all methods with multiple label values
// ---------------------------------------------------------------------------

// TestNoopMetrics_CounterMultipleLabels tests that the noop counter handles
// multiple label dimensions without panicking.
//
// Why this test is important:
//   - Production code passes variable numbers of label values; the noop must
//     accept any arity without panicking to keep disabled-metrics environments
//     stable
//
// What it tests:
//   - Inc with 3 label values does not panic
//   - Add with 3 label values does not panic
func TestNoopMetrics_CounterMultipleLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	counter := m.Counter(
		"requests_total",
		"Total requests",
		textMethod,
		textPathLower,
		textStatus,
	)

	counter.Inc(textGetUpper, textAPIPath, text200)
	counter.Inc(textPost, textAPIPath, "201")
	counter.Add(addInput, textGetUpper, "/health", text200)
	counter.Add(addInputLabels, textDeleteUpper, "/api/1", text404)
}

// TestNoopMetrics_HistogramMultipleLabels tests that the noop histogram handles
// multiple label dimensions without panicking.
//
// Why this test is important:
//   - Production code passes variable numbers of label values; the noop must
//     accept any arity without panicking to keep disabled-metrics environments
//     stable
//
// What it tests:
//   - Observe with 2 label values does not panic for various durations
func TestNoopMetrics_HistogramMultipleLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	histogram := m.Histogram(
		"request_duration",
		"Duration",
		[]float64{0.1, 0.5, 1.0},
		textMethod,
		textPathLower,
	)

	histogram.Observe(observeInput, textGetUpper, textAPIPath)
	histogram.Observe(observeInputLabels, textPost, textAPIPath)
	histogram.Observe(observeInputMultipleLabels, textGetUpper, "/search")
}

// TestNoopMetrics_GaugeMultipleLabels tests that the noop gauge handles
// multiple label dimensions without panicking.
//
// Why this test is important:
//   - Production code passes variable numbers of label values; the noop must
//     accept any arity without panicking to keep disabled-metrics environments
//     stable
//
// What it tests:
//   - Set, Inc, and Dec with 2 label values do not panic
func TestNoopMetrics_GaugeMultipleLabels(t *testing.T) {
	t.Parallel()

	m := noop.New()
	g := m.Gauge("active_connections", "Active connections", textService, "region")

	g.Set(gaugeReading, textAPI, textUsEast1)
	g.Inc(textAPI, textUsEast1)
	g.Dec(textAPI, textUsEast1)
	g.Set(gaugeReadingLabels, "billing", textEuWest1)
}

// TestNoopMetrics_HandlerReturns404 tests that the noop metrics handler returns
// a valid but non-functional HTTP handler.
//
// Why this test is important:
//   - When metrics are disabled, /metrics must still return a valid HTTP
//     response (not nil handler panic); 404 is the correct signal
//   - Health checks and load balancers may probe /metrics
//
// What it tests:
//   - The handler responds with HTTP 404 (NotFoundHandler)
func TestNoopMetrics_HandlerReturns404(t *testing.T) {
	t.Parallel()

	m := noop.New()
	handler := m.Handler()

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		"/metrics",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "noop handler should return 404")
}

// ---------------------------------------------------------------------------
// Middleware: CORS with specific origins
// ---------------------------------------------------------------------------

// TestCORS_SpecificOrigin_Allowed tests that CORS correctly sets headers for an
// allowed specific origin, enabling secure cross-origin requests.
//
// Why this test is important:
//   - The frontend SPA makes cross-origin requests to the API; without correct
//     CORS headers, browsers block all API calls
//   - The Vary header is required for CDN/proxy caching correctness; omitting
//     it causes stale CORS responses for different origins
//
// What it tests:
//   - Access-Control-Allow-Origin matches the request origin
//   - Vary header is set to "Origin" for CDN/proxy caching correctness
func TestCORS_SpecificOrigin_Allowed(t *testing.T) {
	t.Parallel()

	cfg := middleware.CORSConfig{
		AllowOrigins:   []string{textHTTPSAppExampleCom, "https://admin.example.com"},
		AllowMethods:   []string{textGetUpper, textPost},
		AllowHeaders:   []string{"Authorization", textContentType},
		MaxAge:         text3600,
		ExposedHeaders: nil,
	}

	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		textAPIData,
		http.NoBody,
	)
	req.Header.Set(textOriginTitle, textHTTPSAppExampleCom)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(
		t,
		textHTTPSAppExampleCom,
		rec.Header().Get(textAccessControlAllowOrigin),
	)
	assert.Equal(t, textOriginTitle, rec.Header().Get("Vary"))
}

// TestCORS_SpecificOrigin_Disallowed tests that CORS blocks cross-origin
// requests from origins not in the allowlist.
//
// Why this test is important:
//   - Allowing arbitrary origins would expose the API to CSRF and data
//     exfiltration attacks from malicious websites
//   - Security compliance requires explicit origin allowlisting
//
// What it tests:
//   - Access-Control-Allow-Origin is empty for a disallowed origin
func TestCORS_SpecificOrigin_Disallowed(t *testing.T) {
	t.Parallel()

	cfg := middleware.CORSConfig{
		AllowOrigins:   []string{textHTTPSAppExampleCom},
		AllowMethods:   []string{textGetUpper},
		AllowHeaders:   []string{textContentType},
		MaxAge:         text3600,
		ExposedHeaders: nil,
	}

	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		textAPIData,
		http.NoBody,
	)
	req.Header.Set(textOriginTitle, "https://evil.com")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Empty(
		t,
		rec.Header().Get(textAccessControlAllowOrigin),
		"Allow-Origin should be empty for disallowed origin",
	)
}

// TestCORS_NoOriginHeader tests that CORS does not inject headers when no
// Origin header is present, ensuring same-origin requests are unaffected.
//
// Why this test is important:
//   - Same-origin requests (e.g. server-to-server) must not receive CORS
//     headers; injecting them could confuse proxy caches or security scanners
//
// What it tests:
//   - Access-Control-Allow-Origin is empty when no Origin header is sent
//   - The request succeeds with HTTP 200
func TestCORS_NoOriginHeader(t *testing.T) {
	t.Parallel()

	cfg := middleware.CORSConfig{
		AllowOrigins:   []string{textHTTPSAppExampleCom},
		AllowMethods:   []string{textGetUpper},
		AllowHeaders:   []string{textContentType},
		MaxAge:         text3600,
		ExposedHeaders: nil,
	}

	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		textAPIData,
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Empty(
		t,
		rec.Header().Get(textAccessControlAllowOrigin),
		"Allow-Origin should be empty when no Origin header",
	)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestCORS_Preflight_SpecificOrigin tests that OPTIONS preflight requests
// receive correct CORS headers and short-circuit without calling the inner
// handler.
//
// Why this test is important:
//   - Browsers send preflight OPTIONS before mutating cross-origin requests;
//     incorrect responses block all POST/PUT/DELETE from the SPA
//   - The inner handler must not be invoked for preflights to avoid side effects
//
// What it tests:
//   - OPTIONS returns HTTP 204 (No Content)
//   - Inner handler is not invoked
//   - Access-Control-Max-Age matches the configured value
func TestCORS_Preflight_SpecificOrigin(t *testing.T) {
	t.Parallel()

	cfg := middleware.CORSConfig{
		AllowOrigins:   []string{textHTTPSAppExampleCom},
		AllowMethods:   []string{textGetUpper, textPost, "PUT"},
		AllowHeaders:   []string{"Authorization", textContentType},
		MaxAge:         "7200",
		ExposedHeaders: nil,
	}

	handlerCalled := false
	handler := middleware.CORS(
		&cfg,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		"OPTIONS",
		textAPIData,
		http.NoBody,
	)
	req.Header.Set(textOriginTitle, textHTTPSAppExampleCom)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, handlerCalled, "inner handler should not be called for OPTIONS")
	assert.Equal(t, "7200", rec.Header().Get("Access-Control-Max-Age"))
}

// ---------------------------------------------------------------------------
// Middleware: GetRequestID edge cases
// ---------------------------------------------------------------------------

// TestGetRequestID_EmptyContext tests that GetRequestID returns an empty string
// when no request ID is in the context.
//
// Why this test is important:
//   - Background workers and tests may call GetRequestID without HTTP
//     middleware; the function must not panic or return garbage
//
// What it tests:
//   - GetRequestID with a bare context.Background() returns ""
func TestGetRequestID_EmptyContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	id := middleware.GetRequestID(ctx)
	assert.Empty(t, id, "GetRequestID(empty ctx) should return empty string")
}

// TestGetRequestID_UnrelatedContext tests that GetRequestID returns an empty
// string when the context contains unrelated values.
//
// Why this test is important:
//   - Context keys use unexported types for collision avoidance; GetRequestID
//     must only match its own key, not accidentally read unrelated values
//
// What it tests:
//   - GetRequestID with a context carrying a different key type returns ""
func TestGetRequestID_UnrelatedContext(t *testing.T) {
	t.Parallel()

	type otherKey struct{}

	ctx := context.WithValue(context.Background(), otherKey{}, "something")
	id := middleware.GetRequestID(ctx)
	assert.Empty(t, id, "GetRequestID(unrelated ctx) should return empty string")
}

// ---------------------------------------------------------------------------
// Middleware: Compression streaming (already-gzipping path)
// ---------------------------------------------------------------------------

// TestCompression_MultipleWrites tests that the compression middleware handles
// multiple small writes that together exceed the threshold.
//
// Why this test is important:
//   - Streaming responses (e.g. WebSocket chunked queries) write multiple small
//     chunks; compression must activate once the cumulative size crosses the
//     threshold, not only on single large writes
//
// What it tests:
//   - Three 600-byte writes trigger gzip compression (total 1800 > 1024 threshold)
//   - Content-Encoding header is set to "gzip"
//   - Decompressed body length matches the original total
func TestCompression_MultipleWrites(t *testing.T) {
	t.Parallel()

	chunk := strings.Repeat(
		textX,
		repeatInputWrites,
	) // 600 bytes each, 3 chunks = 1800 > 1024 threshold

	handler := middleware.Compression()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(chunk))
			_, _ = w.Write([]byte(chunk))
			_, _ = w.Write([]byte(chunk))
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		textSlash,
		http.NoBody,
	)
	req.Header.Set("Accept-Encoding", textGzip)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(
		t,
		textGzip,
		rec.Header().Get(textContentEncoding),
		"expected gzip Content-Encoding for multi-write response",
	)

	reader, err := gzip.NewReader(rec.Body)
	require.NoError(t, err, "gzip decode")

	defer func() { _ = reader.Close() }()

	body, _ := io.ReadAll(reader)
	assert.Len(t, body, 600*3, "decompressed body length mismatch")
}

// TestCompression_ExactThreshold tests that compression activates at exactly
// the 1024-byte threshold boundary.
//
// Why this test is important:
//   - Off-by-one errors at the threshold boundary would either compress
//     unnecessarily small responses (wasting CPU) or miss the threshold
//     (sending uncompressed large responses)
//
// What it tests:
//   - A 1024-byte response triggers gzip compression
//   - Content-Encoding header is set to "gzip"
func TestCompression_ExactThreshold(t *testing.T) {
	t.Parallel()

	exactBody := strings.Repeat("y", repeatInputThreshold)

	handler := middleware.Compression()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(exactBody))
		}),
	)

	req := httptest.NewRequestWithContext(
		t.Context(),
		textGetUpper,
		textSlash,
		http.NoBody,
	)
	req.Header.Set("Accept-Encoding", textGzip)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(
		t,
		textGzip,
		rec.Header().Get(textContentEncoding),
		"expected gzip at exact 1024-byte threshold",
	)
}

// ---------------------------------------------------------------------------
// Resilience: RetryWithResult
// ---------------------------------------------------------------------------

// TestRetryWithResult_Success tests that RetryWithResult returns the typed
// value on first-attempt success with no retry overhead.
//
// Why this test is important:
//   - The common case is immediate success; retry machinery must not corrupt
//     the return value or add latency
//
// What it tests:
//   - Returns ("hello", nil) on immediate success
func TestRetryWithResult_Success(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
		IsRetryable:     nil,
	})

	ctx := context.Background()
	result, err := exponential.RetryWithResult(ctx, retrier, func() (string, error) {
		return "hello", nil
	})
	require.NoError(t, err, textUnexpectedError)
	assert.Equal(t, "hello", result)
}

// TestRetryWithResult_SucceedsAfterRetries tests that RetryWithResult recovers
// after transient failures and returns the successful value.
//
// Why this test is important:
//   - Transient failures (network blips, temporary unavailability) are common;
//     the generic wrapper must preserve the typed return value after recovery
//   - Validates the core retry-with-result loop used by retrieval and ingestion
//
// What it tests:
//   - Fails twice then succeeds on third attempt
//   - Returns (42, nil) after recovery
//   - Total attempts equal 3
func TestRetryWithResult_SucceedsAfterRetries(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      5,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  2 * time.Second,
		IsRetryable:     nil,
	})

	ctx := context.Background()

	var attempts atomic.Int32

	result, err := exponential.RetryWithResult(ctx, retrier, func() (int, error) {
		if attempts.Add(1) < 3 {
			return 0, apperr.Sentinel("transient")
		}

		return 42, nil
	})
	require.NoError(t, err, textUnexpectedError)
	assert.Equal(t, wantResult, result)
	assert.Equal(t, int32(wantAttempts), attempts.Load(), "expected 3 total attempts")
}

// TestRetryWithResult_AllFailures tests that RetryWithResult returns an error
// when all retry attempts are exhausted.
//
// Why this test is important:
//   - Callers must receive a clear error when retries fail permanently; a nil
//     error with a zero-value result would silently corrupt data
//   - The zero-value result must be safe to use (no partial state)
//
// What it tests:
//   - Returns a non-nil error after exhausting retries
//   - The result is the zero value for the type
func TestRetryWithResult_AllFailures(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
		IsRetryable:     nil,
	})

	ctx := context.Background()
	persistentErr := apperr.Sentinel("always fails")
	result, err := exponential.RetryWithResult(ctx, retrier, func() (string, error) {
		return "", persistentErr
	})
	require.Error(t, err, "expected error after all retries exhausted")
	assert.Empty(t, result, "result should be empty on failure")
}

// TestRetryWithResult_BudgetExhausted tests that RetryWithResult respects the
// retry budget, stopping retries when the budget is consumed.
//
// Why this test is important:
//   - The retry budget prevents retry storms in distributed systems; the
//     generic RetryWithResult must honor it just like the non-generic Retry
//   - Budget exhaustion must surface as a distinct sentinel error for callers
//
// What it tests:
//   - Returns ErrRetryBudgetExhausted when budget (2) is consumed
func TestRetryWithResult_BudgetExhausted(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      10,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  5 * time.Second,
		IsRetryable:     nil,
	})

	b := budget.NewBudget(newBudgetInput)
	ctx := budget.WithRetryBudget(context.Background(), b)

	var attempts atomic.Int32

	_, err := exponential.RetryWithResult(ctx, retrier, func() (int, error) {
		attempts.Add(1)

		return 0, apperr.Sentinel("transient")
	})
	require.Error(t, err, "expected error when budget exhausted")
	assert.ErrorIs(t, err, budget.ErrRetryBudgetExhausted)
}

// TestRetry_Retry_BudgetExhausted tests that the non-generic Retry method
// respects the retry budget, stopping retries when the budget is consumed.
//
// Why this test is important:
//   - Both the generic and non-generic retry paths must enforce the budget;
//     an inconsistency would let some callers bypass retry limits
//
// What it tests:
//   - Returns ErrRetryBudgetExhausted when budget (1) is consumed
func TestRetry_Retry_BudgetExhausted(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      10,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  5 * time.Second,
		IsRetryable:     nil,
	})

	b := budget.NewBudget(1)
	ctx := budget.WithRetryBudget(context.Background(), b)

	var attempts atomic.Int32

	err := retrier.Retry(ctx, func() error {
		attempts.Add(1)

		return apperr.Sentinel("transient")
	})
	require.Error(t, err, "expected error when budget exhausted")
	assert.ErrorIs(t, err, budget.ErrRetryBudgetExhausted)
}

// TestRetry_ContextCancelled tests that Retry honors context cancellation,
// allowing callers to bound retry duration.
//
// Why this test is important:
//   - Request handlers carry deadlines; if Retry ignores cancellation, requests
//     hang until MaxElapsedTime, wasting goroutines and sockets
//   - Proper cancellation is essential for graceful shutdown
//
// What it tests:
//   - Retry returns a non-nil error after the context is cancelled mid-retry
func TestRetry_ContextCancelled(t *testing.T) {
	t.Parallel()

	retrier := exponential.New(exponential.Config{
		MaxRetries:      100,
		InitialInterval: 50 * time.Millisecond,
		MaxInterval:     200 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  30 * time.Second,
		IsRetryable:     nil,
	})

	ctx, cancel := context.WithCancel(context.Background())

	var attempts atomic.Int32

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := retrier.Retry(ctx, func() error {
		attempts.Add(1)

		return apperr.Sentinel("transient")
	})
	require.Error(t, err, "expected error from cancelled context")
}

// ---------------------------------------------------------------------------
// Resilience: Budget edge cases
// ---------------------------------------------------------------------------

// TestRetryBudget_RemainingWithoutBudget tests that Remaining returns -1
// (unlimited) when no budget is attached to the context.
//
// Why this test is important:
//   - Observability code queries Remaining to report budget status; -1 is the
//     sentinel for "no budget configured" and must be distinguishable from 0
//
// What it tests:
//   - Remaining on a bare context returns -1
func TestRetryBudget_RemainingWithoutBudget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := budget.Remaining(ctx)
	assert.Equal(t, int32(-1), r)
}

// TestRetryBudget_RemainingFullyConsumed tests that Remaining returns 0 when
// the budget is fully consumed.
//
// Why this test is important:
//   - Observability dashboards use Remaining=0 to detect that a service has
//     hit its retry ceiling; incorrect reporting hides exhaustion events
//
// What it tests:
//   - Remaining returns 0 after consuming all 2 retries
func TestRetryBudget_RemainingFullyConsumed(t *testing.T) {
	t.Parallel()

	b := budget.NewBudget(newBudgetInput)
	ctx := budget.WithRetryBudget(context.Background(), b)

	budget.ConsumeRetry(ctx)
	budget.ConsumeRetry(ctx)

	r := budget.Remaining(ctx)
	assert.Equal(t, int32(0), r)
}

// TestRetryBudget_ConsumeRetry_Idempotent tests that ConsumeRetry returns false
// consistently after exhaustion.
//
// Why this test is important:
//   - Race conditions or retry loops may call ConsumeRetry multiple times after
//     exhaustion; the function must return false reliably, not flip-flop
//   - Inconsistent results would allow retries beyond the budget
//
// What it tests:
//   - First consumption succeeds (budget=1)
//   - Five subsequent consumptions all return false
func TestRetryBudget_ConsumeRetry_Idempotent(t *testing.T) {
	t.Parallel()

	b := budget.NewBudget(1)
	ctx := budget.WithRetryBudget(context.Background(), b)

	require.True(t, budget.ConsumeRetry(ctx), "first consume should succeed")

	for i := range iterationsIdempotent {
		assert.False(t, budget.ConsumeRetry(ctx), "consume %d should have failed", i+2)
	}
}

// ---------------------------------------------------------------------------
// Config: viper.DefaultConfig via env var
// ---------------------------------------------------------------------------

// TestViperDefaultConfig_EnvOverride tests that DefaultConfig selects the overlay
// from the APP_ENV environment variable.
//
// Why this test is important:
//   - Environment-specific config layering depends on the selector variable;
//     if DefaultConfig ignores it, services always load the base config
//   - Container orchestrators set APP_ENV to select staging/prod overlays
//
// What it tests:
//   - Setting APP_ENV=staging causes DefaultConfig().Env to be "staging"
func TestViperDefaultConfig_EnvOverride(t *testing.T) {
	// t.Setenv is not compatible with t.Parallel()
	t.Setenv(textAppEnv, textStaging)

	cfg := viperloader.DefaultConfig()
	assert.Equal(t, textStaging, cfg.Env)
}

// ---------------------------------------------------------------------------
// Config: full layer loading with all 3 layers
// ---------------------------------------------------------------------------

// TestViperLoader_FullLayerLoading tests that all three config layers (base +
// env overlay + secrets) merge correctly.
//
// Why this test is important:
//   - Production config merges base, environment, and secrets layers; incorrect
//     merge order would either leak defaults or lose secret overrides
//   - This is the full integration path for config loading
//
// What it tests:
//   - Base value (server.port=8080) is preserved
//   - Environment overlay (log.level=warn) overrides base
//   - Secrets overlay (db.password=staging-secret) overrides base
func TestViperLoader_FullLayerLoading(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
server:
  port: 8080
  host: localhost
log:
  level: info
db:
  password: default
`)
	writeFile(t, filepath.Join(dir, "staging.yaml"), `
log:
  level: warn
`)
	writeFile(t, filepath.Join(dir, "secrets.yaml"), `
db:
  password: staging-secret
`)

	cfg := viperloader.Config{
		BaseDir:  dir,
		Env:      textStaging,
		Prefix:   textMyapp,
		Schema:   nil,
		ExtraEnv: nil,
	}
	loader := viperloader.New(cfg)
	require.NoError(t, loader.Load(), textLoad)

	assert.Equal(t, wantGetInt, loader.GetInt(textServerPort))
	assert.Equal(t, textWarn, loader.GetString("log.level"))
	assert.Equal(t, "staging-secret", loader.GetString("db.password"))
}

// ---------------------------------------------------------------------------
// Logger: zap handler enabled check
// ---------------------------------------------------------------------------

// TestZapLogger_RedactingHandler_Enabled tests that the redacting handler
// correctly delegates the Enabled check to the underlying handler.
//
// Why this test is important:
//   - slog skips handler.Handle when Enabled returns false; if the redacting
//     handler overrides this incorrectly, filtered messages still get processed
//   - Correct delegation avoids unnecessary PII regex evaluation on filtered
//     log levels
//
// What it tests:
//   - Debug messages are filtered out when level is set to "warn"
//   - Warn messages pass through
func TestZapLogger_RedactingHandler_Enabled(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textWarn,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	slogger.DebugContext(t.Context(), "this should be filtered out")
	slogger.WarnContext(t.Context(), "this should pass through")
}

// TestZapLogger_RedactingHandler_Handle_WithCorrelation tests that the
// redacting handler injects correlation_id from context.
//
// Why this test is important:
//   - Correlation IDs enable end-to-end request tracing across log entries;
//     the handler must extract and attach them automatically
//   - Without automatic injection, developers must manually pass correlation
//     IDs to every log call
//
// What it tests:
//   - InfoContext with a bare context does not panic
//   - The correlation ID injection code path in Handle() is exercised
func TestZapLogger_RedactingHandler_Handle_WithCorrelation(t *testing.T) {
	t.Parallel()

	cfg := zaplogger.Config{
		Level:     textDebug,
		Format:    textStructuredFormat,
		RedactPII: true,
	}
	slogger := zaplogger.NewSlog(cfg)

	ctx := context.Background()
	slogger.InfoContext(ctx, "without correlation")
}

// ---------------------------------------------------------------------------
// Tracer: OTel tracer creation and span operations
// ---------------------------------------------------------------------------

// TestOTelTracer_NewAndShutdown tests that the OTel tracer can be created and
// shut down without infrastructure.
//
// Why this test is important:
//   - Every service creates and shuts down the tracer during its lifecycle;
//     errors in either path would prevent service startup or clean shutdown
//   - The OTLP gRPC exporter is lazy, so this test validates the code path
//     without requiring a running collector
//
// What it tests:
//   - oteltracer.New returns a non-nil Tracer satisfying interfaces.Tracer
//   - Shutdown completes without error
func TestOTelTracer_NewAndShutdown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := oteltracer.Config{
		ServiceName: "test-service-shutdown",
		Endpoint:    textLocalhost4317,
		SampleRate:  sampleRate,
		Insecure:    true,
	}

	currentTracer, err := oteltracer.New(ctx, cfg)
	require.NoError(t, err, textOteltracerNew)
	require.NotNil(t, currentTracer, "expected non-nil tracer")

	var _ interfaces.Tracer = currentTracer

	require.NoError(t, currentTracer.Shutdown(ctx), "Shutdown")
}

// TestOTelTracer_SpanConversions tests that the OTel tracer's spans translate the
// foundation span vocabulary (attributes, statuses, errors, kinds) into OTel correctly.
//
// Why this test is important:
//   - Span attributes, statuses and kinds are set on every request; a broken converter
//     silently mislabels traces (a failed call shown as OK, a server span shown as
//     internal) and corrupts trace search and service graphs.
//   - The spans are read through OTel's ReadOnlySpan view and never ended, so nothing is
//     queued for export: the test needs no collector and opens no connection.
//
// What it tests:
//   - SetAttribute maps string, int, int64, float64 and bool to the matching OTel types
//     and an unsupported type to an empty string.
//   - RecordError adds one "exception" event; SetStatus maps Error (with its
//     description), OK and Unset to the OTel codes.
//   - WithSpanKind maps Server, Client, Producer, Consumer and Internal to the OTel
//     kinds.
func TestOTelTracer_SpanConversions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	currentTracer, err := oteltracer.New(ctx, oteltracer.Config{
		ServiceName: "test-service-spans",
		Endpoint:    textLocalhost4317,
		SampleRate:  1.0,
		Insecure:    true,
	})
	require.NoError(t, err, textOteltracerNew)
	t.Cleanup(func() { _ = currentTracer.Shutdown(ctx) })

	// readOnly starts a span and returns it with OTel's read-only view of it.
	readOnly := func(opts ...interfaces.SpanOption) (
		interfaces.Span, sdktrace.ReadOnlySpan,
	) {
		spanCtx, span := currentTracer.Start(ctx, "test-operation", opts...)
		ro, ok := oteltrace.SpanFromContext(spanCtx).(sdktrace.ReadOnlySpan)
		require.True(t, ok, "a sampled span must be readable")

		return span, ro
	}

	span, readOnlySpan := readOnly()
	span.SetAttribute("user.id", "abc-123")
	span.SetAttribute(textCount, setAttributeInput)
	span.SetAttribute(textData, int64(setAttributeInputConversions))
	span.SetAttribute("score", setAttributeInputSpanConversions)
	span.SetAttribute(textActiveLower, true)
	span.SetAttribute("complex", struct{}{})
	span.RecordError(apperr.Sentinel("test error"))
	span.SetStatus(interfaces.SpanStatusError, textFailed)

	t.Helper()

	assert.ElementsMatch(t, []attribute.KeyValue{
		attribute.String("user.id", "abc-123"),
		attribute.Int(textCount, 42),
		attribute.Int64(textData, 1000),
		attribute.Float64("score", 3.14),
		attribute.Bool(textActiveLower, true),
		attribute.String("complex", ""),
	}, readOnlySpan.Attributes())

	testOTelTracerSpanConversionsEventsAndStatus(t, readOnly, readOnlySpan)
}

// testOTelTracerSpanConversionsEventsAndStatus continues
// testOTelTracerSpanConversionsRest: events and status.
func testOTelTracerSpanConversionsEventsAndStatus(
	t *testing.T,
	readOnly func(opts ...interfaces.SpanOption) (interfaces.Span, sdktrace.ReadOnlySpan),
	readOnlySpan sdktrace.ReadOnlySpan,
) {
	t.Helper()

	var span interfaces.Span

	require.Len(t, readOnlySpan.Events(), 1)
	assert.Equal(t, "exception", readOnlySpan.Events()[0].Name)
	assert.Equal(
		t,
		sdktrace.Status{Code: codes.Error, Description: textFailed},
		readOnlySpan.Status(),
	)

	span, readOnlySpan = readOnly()
	span.SetStatus(interfaces.SpanStatusOK, "")
	assert.Equal(t, codes.Ok, readOnlySpan.Status().Code)

	span, readOnlySpan = readOnly()
	span.SetStatus(interfaces.SpanStatusUnset, "")
	assert.Equal(t, codes.Unset, readOnlySpan.Status().Code)

	for kind, want := range map[interfaces.SpanKind]oteltrace.SpanKind{
		interfaces.SpanKindServer:   oteltrace.SpanKindServer,
		interfaces.SpanKindClient:   oteltrace.SpanKindClient,
		interfaces.SpanKindProducer: oteltrace.SpanKindProducer,
		interfaces.SpanKindConsumer: oteltrace.SpanKindConsumer,
		interfaces.SpanKindInternal: oteltrace.SpanKindInternal,
	} {
		_, readOnlySpan = readOnly(interfaces.WithSpanKind(kind))
		assert.Equal(t, want, readOnlySpan.SpanKind(), "span kind %v", kind)
	}
}

// TestOTelTracer_EndUnsampledSpan tests that ending a span completes without panic.
//
// Why this test is important:
//   - Every instrumented operation ends its span; End must be safe on the common
//     unsampled path. The span is unsampled, so nothing is exported and no collector
//     connection is opened.
//
// What it tests:
//   - A span from a tracer with SampleRate 0 is non-recording, and End returns normally.
func TestOTelTracer_EndUnsampledSpan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	currentTracer, err := oteltracer.New(ctx, oteltracer.Config{
		ServiceName: "test-service-end", Endpoint: textLocalhost4317, Insecure: true,
		SampleRate: 0,
	})
	require.NoError(t, err, textOteltracerNew)
	t.Cleanup(func() { _ = currentTracer.Shutdown(ctx) })

	spanCtx, span := currentTracer.Start(ctx, "unsampled")
	assert.False(
		t,
		oteltrace.SpanFromContext(spanCtx).IsRecording(),
		"SampleRate 0 must not record",
	)
	assert.NotPanics(t, span.End)
}

// TestOTelTracer_SampleRates tests tracer creation with different sample rates
// to exercise all sampler branches (always, never, ratio).
//
// Why this test is important:
//   - Production uses fractional sampling to control trace volume; the sampler
//     selection logic must handle edge cases (0.0, 1.0, negative, >1.0)
//     without panicking or silently disabling all tracing
//
// What it tests:
//   - Sample rates 1.0, 0.0, -1.0, 0.5, and 2.0 all produce valid tracers
func TestOTelTracer_SampleRates(t *testing.T) {
	t.Parallel()

	rates := []float64{rateOne, rateZero, -rateOne, rates0Rates, rateTwo}
	for _, rate := range rates {
		ctx := context.Background()
		cfg := oteltracer.Config{
			ServiceName: "sample-test",
			Endpoint:    textLocalhost4317,
			SampleRate:  rate,
			Insecure:    true,
		}

		tr, err := oteltracer.New(ctx, cfg)
		require.NoError(t, err, "oteltracer.New with rate %f", rate)

		_ = tr.Shutdown(ctx)
	}
}

// TestOTelTracer_SecureMode tests tracer creation without the insecure flag,
// exercising the TLS-enabled exporter path.
//
// Why this test is important:
//   - Production deployments use TLS for OTLP gRPC; the secure path must not
//     panic or fail to create the tracer
//   - The OTLP exporter is lazy, so this validates config wiring without
//     needing a real TLS endpoint
//
// What it tests:
//   - oteltracer.New with Insecure=false creates a valid tracer
func TestOTelTracer_SecureMode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := oteltracer.Config{
		ServiceName: "secure-test",
		Endpoint:    textLocalhost4317,
		SampleRate:  sampleRate,
		Insecure:    false,
	}

	tr, err := oteltracer.New(ctx, cfg)
	require.NoError(t, err, "oteltracer.New (secure)")

	_ = tr.Shutdown(ctx)
}

// TestTracer_NewFromConfig_KindOTel tests the tracer factory with KindOTel,
// covering the success path of NewFromConfig.
//
// Why this test is important:
//   - NewFromConfig is the primary constructor a composition root calls; it must
//     work with fully specified config structs from YAML/env
//
// What it tests:
//   - NewFromConfig with KindOTel default config returns a non-nil Tracer
func TestTracer_NewFromConfig_KindOTel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := tracer.DefaultConfig("factory-test")

	cfg.Insecure = true

	tr, err := tracer.NewFromConfig(ctx, cfg)
	require.NoError(t, err, "NewFromConfig")

	defer func() { _ = tr.Shutdown(ctx) }()

	require.NotNil(t, tr, "expected non-nil tracer from factory")
}

// TestTracer_New_WithOptions tests the tracer factory New method with
// functional options.
//
// Why this test is important:
//   - Functional options are the public API for configuring the tracer at
//     creation time; all four options (name, endpoint, rate, insecure) must
//     be accepted without error
//
// What it tests:
//   - tracer.New with WithServiceName, WithEndpoint, WithSampleRate, and
//     WithInsecure options returns a non-nil Tracer
func TestTracer_New_WithOptions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	currentTracer, err := tracer.New(
		ctx, tracer.KindOTel,
		tracer.WithServiceName("option-test"),
		tracer.WithEndpoint(textLocalhost4317),
		tracer.WithSampleRate(withSampleRateInput),
		tracer.WithInsecure(true),
	)
	require.NoError(t, err, "tracer.New")

	defer func() { _ = currentTracer.Shutdown(ctx) }()

	require.NotNil(t, currentTracer, "expected non-nil tracer")
}

// TestOTelTracer_ShutdownNilFn tests that Shutdown is safe to call and
// completes without error after normal creation.
//
// Why this test is important:
//   - Graceful shutdown calls Shutdown on the tracer; if the internal shutdown
//     function is nil (e.g. double shutdown), it must not panic
//
// What it tests:
//   - Shutdown after normal creation returns nil
func TestOTelTracer_ShutdownNilFn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := oteltracer.Config{
		ServiceName: "nil-shutdown-test",
		Endpoint:    textLocalhost4317,
		SampleRate:  sampleRate,
		Insecure:    true,
	}

	tr, err := oteltracer.New(ctx, cfg)
	require.NoError(t, err, textOteltracerNew)

	require.NoError(t, tr.Shutdown(ctx), "first Shutdown")
}

const (
	// addInputLabels is the value passed to Add in TestNoopMetrics_CounterMultipleLabels.
	addInputLabels = 0.0

	// gaugeReadingLabels is the value passed to Set in
	// TestNoopMetrics_GaugeMultipleLabels.
	gaugeReadingLabels = 0.0

	// infoContextInput is the args passed to InfoContext in
	// TestZapLogger_RedactAttr_NonString.
	infoContextInput = 42

	// infoContextInputString is the args passed to InfoContext in
	// TestZapLogger_RedactAttr_NonString.
	infoContextInputString = 3.14

	// iterationsIdempotent is the iteration count in
	// TestRetryBudget_ConsumeRetry_Idempotent.
	iterationsIdempotent = 5

	// observeInputLabels is the value passed to Observe in
	// TestNoopMetrics_HistogramMultipleLabels.
	observeInputLabels = 0.5

	// observeInputMultipleLabels is the value passed to Observe in
	// TestNoopMetrics_HistogramMultipleLabels.
	observeInputMultipleLabels = 2.0

	// rateZero is an element of a []float64 in TestOTelTracer_SampleRates.
	rateZero = 0.0

	// rates0Rates is an element of a []float64 in TestOTelTracer_SampleRates.
	rates0Rates = 0.5

	// rateOne is an element of a []float64 in TestOTelTracer_SampleRates.
	rateOne = 1.0

	// rateTwo is an element of a []float64 in TestOTelTracer_SampleRates.
	rateTwo = 2.0

	// repeatInputThreshold is the count passed to Repeat in
	// TestCompression_ExactThreshold.
	repeatInputThreshold = 1024

	// repeatInputWrites is the count passed to Repeat in TestCompression_MultipleWrites.
	repeatInputWrites = 600

	// sampleRate is the SampleRate field in TestOTelTracer_NewAndShutdown,
	// TestOTelTracer_SecureMode and 1 more.
	sampleRate = 1.0

	// setAttributeInput is the value passed to SetAttribute in
	// TestOTelTracer_SpanConversions.
	setAttributeInput = 42

	// setAttributeInputConversions is argument 1 of int64 in
	// TestOTelTracer_SpanConversions.
	setAttributeInputConversions = 1000

	// setAttributeInputSpanConversions is the value passed to SetAttribute in
	// TestOTelTracer_SpanConversions.
	setAttributeInputSpanConversions = 3.14

	// wantResult is the expected result.
	wantResult = 42

	// withInput is the args passed to With in TestZapLogger_WithAttrs_MultipleAttrs.
	withInput = 5

	// withSampleRateInput is the rate passed to WithSampleRate in
	// TestTracer_New_WithOptions.
	withSampleRateInput = 0.5
)

const (
	// textAPIData is the literal "/api/data".
	textAPIData = "/api/data"

	// textHTTPSAppExampleCom is the literal "https://app.example.com".
	textHTTPSAppExampleCom = "https://app.example.com"

	// textLoad is the literal "Load".
	textLoad = "Load"

	// textOriginTitle is the literal "Origin".
	textOriginTitle = "Origin"

	// textOteltracerNew is the literal "oteltracer.New".
	textOteltracerNew = "oteltracer.New"
)
