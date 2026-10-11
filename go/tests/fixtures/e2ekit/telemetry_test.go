// Package e2ekit_test verifies the gen-free telemetry assertions an endpoint E2E suite
// shares across a CLI harness and services' //go:build e2e tests. They mock the small
// HTTP boundary so the Tempo/Prometheus/Loki query construction + response parsing are
// tested without live backends.
package e2ekit_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/e2ekit"
)

// doerFunc adapts a function to e2ekit.HTTPDoer — a mock of the small consumer-side HTTP
// boundary so the telemetry queries + response parsing are tested without live
// Tempo/Prometheus/Loki.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// jsonResponse builds a canned *http.Response with the given status and body.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestTraceIDFromHeader tests extraction of the trace id from a traceresponse header.
//
// Why this test is important:
//   - The whole telemetry proof keys off the trace id the endpoint returns; a wrong parse
//     correlates nothing. Malformed input must yield "" rather than a bogus id.
//
// What it tests:
//   - A valid W3C traceparent yields its 32-hex trace id; empty/garbage/short input
//     yields "".
func TestTraceIDFromHeader(t *testing.T) {
	assert.Equal(
		t,
		"0123456789abcdef0123456789abcdef",
		e2ekit.TraceIDFromHeader(
			"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		),
	)
	assert.Empty(t, e2ekit.TraceIDFromHeader(""))
	assert.Empty(t, e2ekit.TraceIDFromHeader("garbage"))
	assert.Empty(t, e2ekit.TraceIDFromHeader("00-nothex-0123456789abcdef-01"))
}

// TestForceSampledTraceparent tests that the E2E force-sample header is a well-formed W3C
// traceparent with the sampled flag set and round-trips through TraceIDFromHeader.
//
// Why this test is important:
//   - The header is the lever that guarantees an endpoint-E2E request's trace is recorded
//     by the server's ParentBased sampler and thus reaches Tempo. If the sampled flag
//     were unset or the format malformed, the server would fall back to the ratio and the
//     trace could be dropped. Random ids must not collide across concurrent runs.
//
// What it tests:
//   - The value matches `00-<32hex>-<16hex>-01` (sampled flag 01); TraceIDFromHeader
//     recovers the same trace id it returns; two calls produce distinct trace ids.
func TestForceSampledTraceparent(t *testing.T) {
	header, traceID, err := e2ekit.ForceSampledTraceparent()
	require.NoError(t, err)

	parts := strings.Split(header, "-")
	require.Len(t, parts, 4, "traceparent has four dash-delimited fields")
	assert.Equal(t, "00", parts[0], "version")
	assert.Len(t, parts[1], 32, "trace id is 32 hex")
	assert.Len(t, parts[2], 16, "span id is 16 hex")
	assert.Equal(t, "01", parts[3], "sampled flag must be set")
	assert.Equal(
		t,
		traceID,
		e2ekit.TraceIDFromHeader(header),
		"round-trips through TraceIDFromHeader",
	)

	_, other, err := e2ekit.ForceSampledTraceparent()
	require.NoError(t, err)
	assert.NotEqual(t, traceID, other, "each call yields a distinct random trace id")
}

// TestTelemetryClient_TraceInTempo tests the Tempo trace lookup.
//
// Why this test is important:
//   - Proving a request's spans landed in Tempo is the core "tracing round-trip"
//     assertion; the query URL and batch-count parse must be exact.
//
// What it tests:
//   - A 200 with N batches returns N, and the request targets /api/traces/{id}.
func TestTelemetryClient_TraceInTempo(t *testing.T) {
	var gotURL string
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return jsonResponse(http.StatusOK, `{"batches":[{},{}]}`), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo:3200",
		"http://prom:9090",
		"http://loki:3100",
	)

	n, err := c.TraceInTempo(context.Background(), "abc123")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "http://tempo:3200/api/traces/abc123", gotURL)
}

// TestTelemetryClient_TraceInTempo_NotFound tests that a not-yet-present trace is a zero
// result, not an error (so the caller can poll).
//
// Why this test is important:
//   - Spans reach Tempo a few seconds after the RPC; a 404 must be "not yet", not a
//     failure, or every poll would abort on the first miss.
//
// What it tests:
//   - A 404 returns (0, nil).
func TestTelemetryClient_TraceInTempo_NotFound(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, ``), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo",
		"http://prom",
		"http://loki",
	)

	n, err := c.TraceInTempo(context.Background(), "missing")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// TestTelemetryClient_TraceSpansService tests that a trace is recognized as spanning a
// given service.
//
// Why this test is important:
//   - A cross-service telemetry proof asserts the trace actually REACHED a service (e.g.
//     a trace that crossed into a downstream `worker`), not merely that some spans exist.
//     The check scans each Tempo batch's `service.name` resource attribute; matching the
//     wrong key/value would pass a trace that never touched the service.
//
// What it tests:
//   - A trace whose batch resource carries service.name=worker returns true; a different
//     service returns false; an absent trace (404) returns false without error (so
//     callers can poll).
func TestTelemetryClient_TraceSpansService(t *testing.T) {
	body := `{"batches":[{"resource":{"attributes":[` +
		`{"key":"service.name","value":{"stringValue":"worker"}}]}}]}`
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo:3200",
		"http://prom",
		"http://loki",
	)

	spans, err := c.TraceSpansService(context.Background(), "abc123", "worker")
	require.NoError(t, err)
	assert.True(t, spans, "the trace spans service.name=worker")

	other, err := c.TraceSpansService(context.Background(), "abc123", "gateway")
	require.NoError(t, err)
	assert.False(t, other, "the trace does not span service.name=gateway")

	missDoer := doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, ``), nil
	})
	cMiss := e2ekit.NewTelemetryClient(
		missDoer,
		"http://tempo",
		"http://prom",
		"http://loki",
	)
	absent, err := cMiss.TraceSpansService(context.Background(), "missing", "worker")
	require.NoError(t, err)
	assert.False(t, absent, "an absent trace is false, not an error (poll case)")
}

// TestTelemetryClient_MetricValue tests the Prometheus instant-query value parse.
//
// Why this test is important:
//   - Proving a RED metric was recorded/scraped needs the sample value pulled from the
//     [<ts>, "<number>"] shape; parsing the wrong element silently passes on any value.
//
// What it tests:
//   - A single-result instant vector returns the numeric sample; the query is URL-encoded
//     onto /api/v1/query.
func TestTelemetryClient_MetricValue(t *testing.T) {
	var gotURL string
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return jsonResponse(
			http.StatusOK,
			`{"data":{"result":[{"value":[123.4,"5"]}]}}`,
		), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo",
		"http://prom:9090",
		"http://loki",
	)

	value, err := c.MetricValue(context.Background(), `http_requests_total{code="200"}`)
	require.NoError(t, err)
	assert.InDelta(t, 5.0, value, 0)
	assert.Contains(t, gotURL, "http://prom:9090/api/v1/query?query=")
	assert.Contains(t, gotURL, url.QueryEscape(`http_requests_total{code="200"}`))
}

// TestTelemetryClient_MetricValue_Empty tests that an empty vector is a zero result, not
// an error.
//
// Why this test is important:
//   - A just-emitted metric may not be scraped yet; an empty result must let the caller
//     poll.
//
// What it tests:
//   - An empty result array returns (0, nil).
func TestTelemetryClient_MetricValue_Empty(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"data":{"result":[]}}`), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo",
		"http://prom",
		"http://loki",
	)

	value, err := c.MetricValue(context.Background(), "up")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, value, 0)
}

// TestTelemetryClient_LogHasTrace tests the Loki log lookup by trace id.
//
// Why this test is important:
//   - Proving the structured log shipped with the trace id attached needs the LogQL to
//     filter on the id (`|= "<id>"`) and the line count summed across streams.
//
// What it tests:
//   - The query carries the trace-id filter, and the returned line count sums the stream
//     values.
func TestTelemetryClient_LogHasTrace(t *testing.T) {
	var gotQuery string
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotQuery = r.URL.Query().Get("query")
		return jsonResponse(
			http.StatusOK,
			`{"data":{"result":[{"values":[["1","a"],["2","b"]]}]}}`,
		), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo",
		"http://prom",
		"http://loki:3100",
	)

	n, err := c.LogHasTrace(context.Background(), `{service="api"}`, "traceid123")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Contains(t, gotQuery, "traceid123")
	assert.Contains(t, gotQuery, "|=")
}

// TestTelemetryClient_QueryError tests that a non-2xx (non-404) backend response surfaces
// as an error.
//
// Why this test is important:
//   - A malformed query (Prometheus/Loki 400) must fail loudly, not be mistaken for "no
//     data yet".
//
// What it tests:
//   - A 400 response returns a non-nil error.
func TestTelemetryClient_QueryError(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, `bad query`), nil
	})
	c := e2ekit.NewTelemetryClient(
		doer,
		"http://tempo",
		"http://prom",
		"http://loki",
	)

	_, err := c.MetricValue(context.Background(), "bad{")
	require.Error(t, err)
}

// TestPollHelpers tests the two poll loops a suite waits on: PollUntil (an async effect)
// and PollTraceInTempo (a trace landing in Tempo).
//
// Why this test is important:
//   - Every async E2E assertion waits through these loops; returning early on a miss,
//     swallowing the last error at the deadline, or ignoring cancellation turns a real
//     failure into a pass or a hang.
//
// What it tests:
//   - PollUntil returns nil as soon as check reports true.
//   - Past the deadline it returns CodeUnavailable: wrapping the last check error when
//     there is one, otherwise naming what did not materialize.
//   - A cancelled ctx stops the wait with ctx.Err().
//   - PollTraceInTempo returns nil once Tempo holds a batch for the trace.
func TestPollHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	errCheck := apperr.Sentinel("backend not ready")

	t.Run("PollUntil returns once check succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := e2ekit.PollUntil(
			ctx,
			time.Minute,
			"effect",
			func(context.Context) (bool, error) {
				calls++
				return true, nil
			},
		)
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("PollUntil wraps the last error at the deadline", func(t *testing.T) {
		t.Parallel()
		err := e2ekit.PollUntil(ctx, 0, "effect", func(context.Context) (bool, error) {
			return false, errCheck
		})
		require.ErrorIs(t, err, errCheck)
		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	})

	t.Run("PollUntil names a miss at the deadline", func(t *testing.T) {
		t.Parallel()
		err := e2ekit.PollUntil(ctx, 0, "effect", func(context.Context) (bool, error) {
			return false, nil
		})
		require.Error(t, err)
		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
		assert.Contains(t, err.Error(), "effect did not materialize")
	})

	t.Run("PollUntil stops on cancellation", func(t *testing.T) {
		t.Parallel()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		err := e2ekit.PollUntil(
			cctx,
			time.Minute,
			"effect",
			func(context.Context) (bool, error) {
				return false, nil
			},
		)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("PollTraceInTempo returns once the trace lands", func(t *testing.T) {
		t.Parallel()
		doer := doerFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"batches":[{}]}`), nil
		})
		c := e2ekit.NewTelemetryClient(doer, "http://tempo", "http://prom", "http://loki")
		require.NoError(t, e2ekit.PollTraceInTempo(ctx, c, "abc123"))
	})
}
