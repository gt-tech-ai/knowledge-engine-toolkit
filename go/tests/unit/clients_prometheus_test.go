package unit_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus"
	promhttp "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus/httpapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// promMatrix is a successful range-query envelope; %s is replaced by the series list.
const promMatrix = `{"status":"success","data":{"resultType":"matrix","result":[%s]}}`

// promVector is a successful instant-query envelope; %s is replaced by the series list.
const promVector = `{"status":"success","data":{"resultType":"vector","result":[%s]}}`

// promResponse builds an HTTP response with status and body.
func promResponse(status int, body string) *http.Response {
	return fixtures.BuildRef(func(row *http.Response) {
		row.StatusCode = status
		row.Body = io.NopCloser(strings.NewReader(body))
	})
}

// doClosed sends req through doer, closes any response body, and returns the error.
func doClosed(doer promhttp.Doer, req *http.Request) error {
	resp, err := doer.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	return err
}

// promSeries builds one matrix series with a sample per value, an hour apart from
// t=1727260000.
func promSeries(vals ...string) string {
	pts := make([]string, 0, len(vals))
	for i, v := range vals {
		pts = append(pts, `[`+[]string{"1727260000", "1727263600"}[i]+`,"`+v+`"]`)
	}

	return `{"metric":{},"values":[` + strings.Join(pts, ",") + `]}`
}

// promInstant builds one vector series with a single sample.
func promInstant(v string) string {
	return `{"metric":{},"value":[1727260000,"` + v + `"]}`
}

// TestClient_QueryParsesVector tests the instant query's contract: one finite sample is
// the value; anything else is a coded error.
//
// Why this test is important:
//   - A health or SLO number built from several un-aggregated series, or one that turned
//     "no data" into 0, would report a value Prometheus never computed.
//
// What it tests:
//   - The request is a GET of <base>/api/v1/query (trailing slash trimmed) with the
//     PromQL.
//   - One sample returns its value; no sample and a NaN are CodeNotFound; two series and
//     a non-vector result are CodeInvalidInput.
func TestClient_QueryParsesVector(t *testing.T) {
	t.Parallel()

	cases := testClientQueryParsesVectorTable()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))

			var req *http.Request

			doer.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(r *http.Request) (*http.Response, error) {
					req = r

					return promResponse(http.StatusOK, testCase.body), nil
				})

			got, err := promhttp.New("http://prom.test:9090/", doer).
				Query(t.Context(), `sum(up)`)

			require.NotNil(t, req)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "prom.test:9090", req.URL.Host)
			assert.Equal(t, "/api/v1/query", req.URL.Path)
			assert.Equal(t, `sum(up)`, req.URL.Query().Get("query"))

			if testCase.wantCode != "" {
				require.Error(t, err)
				assert.Equal(t, testCase.wantCode, apperr.Code(err))

				return
			}

			require.NoError(t, err)
			assert.InDelta(t, testCase.want, got, 0)
		})
	}
}

// testClientQueryParsesVectorRow is one row of testClientQueryParsesVectorTable.
type testClientQueryParsesVectorRow struct {
	name     string
	body     string
	wantCode apperr.ErrorCode
	want     float64
}

// testClientQueryParsesVectorTable builds the table TestClient_QueryParsesVector runs.
func testClientQueryParsesVectorTable() []testClientQueryParsesVectorRow {
	return []testClientQueryParsesVectorRow{
		{
			name:     "one sample",
			body:     strings.Replace(promVector, textSLower, promInstant("12.5"), 1),
			want:     wantTable,
			wantCode: "",
		},
		{
			name:     "no sample",
			body:     strings.Replace(promVector, textSLower, "", 1),
			wantCode: apperr.CodeNotFound,
			want:     0,
		},
		{
			name:     textNan,
			body:     strings.Replace(promVector, textSLower, promInstant(textNan), 1),
			wantCode: apperr.CodeNotFound,
			want:     0,
		},
		{
			name: "two series",
			body: strings.Replace(
				promVector,
				textSLower,
				promInstant("1")+","+promInstant("2"),
				1,
			),
			wantCode: apperr.CodeInvalidInput,
			want:     0,
		},
		{
			name:     "matrix",
			body:     strings.Replace(promMatrix, textSLower, "", 1),
			wantCode: apperr.CodeInvalidInput,
			want:     0,
		},
	}
}

// TestClient_QueryRangeParsesMatrix tests the range query's contract: one series of
// finite samples is the series; anything else is an error.
//
// Why this test is important:
//   - A trend chart built from several un-aggregated series, or one that turned NaN into
//     0, would show a number Prometheus never computed.
//
// What it tests:
//   - The request is a GET of <base>/api/v1/query_range with the PromQL, start, end and
//     step.
//   - A one-series matrix returns its samples in order with their timestamps; an empty
//     matrix is an empty series.
//   - Two series is CodeInvalidInput and a NaN sample CodeNotFound.
func TestClient_QueryRangeParsesMatrix(t *testing.T) {
	t.Parallel()

	instant := time.Unix(1727260000, 0).UTC()
	cases := testClientQueryRangeParsesMatrixTable(instant)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))

			var req *http.Request

			doer.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(r *http.Request) (*http.Response, error) {
					req = r

					return promResponse(http.StatusOK, testCase.body), nil
				})

			got, err := promhttp.New("http://prom.test:9090/", doer).QueryRange(
				t.Context(),
				`sum(increase(x[1h]))`, instant, instant.Add(time.Hour), time.Hour,
			)

			require.NotNil(t, req)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/api/v1/query_range", req.URL.Path)

			q := req.URL.Query()
			assert.Equal(t, `sum(increase(x[1h]))`, q.Get("query"))
			assert.Equal(t, "1727260000", q.Get("start"))
			assert.Equal(t, "1727263600", q.Get("end"))
			assert.Equal(t, text3600, q.Get("step"))

			if testCase.wantCode != "" {
				require.Error(t, err)
				assert.Equal(t, testCase.wantCode, apperr.Code(err))

				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, got)
		})
	}
}

// testClientQueryRangeParsesMatrixRow is one row of
// testClientQueryRangeParsesMatrixTable.
type testClientQueryRangeParsesMatrixRow struct {
	name     string
	body     string
	wantCode apperr.ErrorCode
	want     []promhttp.Sample
}

// testClientQueryRangeParsesMatrixTable builds the table
// TestClient_QueryRangeParsesMatrix runs.
func testClientQueryRangeParsesMatrixTable(
	instant time.Time,
) []testClientQueryRangeParsesMatrixRow {
	return []testClientQueryRangeParsesMatrixRow{
		{
			name: "one series",
			body: strings.Replace(promMatrix, textSLower, promSeries("1.5", "2"), 1),
			want: []promhttp.Sample{
				{Time: instant, Value: valueTable},
				{Time: instant.Add(time.Hour), Value: valueMatrixTable},
			},
			wantCode: "",
		},
		{
			name:     "no series",
			body:     strings.Replace(promMatrix, textSLower, "", 1),
			want:     []promhttp.Sample{},
			wantCode: "",
		},
		{
			name: "two series",
			body: strings.Replace(
				promMatrix,
				textSLower,
				promSeries("1")+","+promSeries("2"),
				1,
			),
			wantCode: apperr.CodeInvalidInput,
			want:     nil,
		},
		{
			name:     textNan,
			body:     strings.Replace(promMatrix, textSLower, promSeries(textNan), 1),
			wantCode: apperr.CodeNotFound,
			want:     nil,
		},
	}
}

// TestClient_ErrorStatusIsCoded tests that every failure on the request path carries the
// code that tells a caller whether to retry, fix the query, or report the server.
//
// Why this test is important:
//   - The client stack retries every code except the permanent ones (invalid input, not
//     found, auth, conflict, exhausted quota) and a caller's cancellation, and its
//     breaker counts the retryable ones. A bad query coded as an outage is retried and
//     trips the breaker; an outage coded as a bad query is never retried; a caller's own
//     deadline coded as an outage blames a healthy server.
//
// What it tests:
//   - A transport error is CodeUnavailable; the caller's cancellation is CodeCanceled and
//     its deadline CodeTimeout.
//   - A throttling or unavailable status (429, 502, 503) is CodeUnavailable; a rejected
//     query (400) is CodeInvalidInput with the server's message; any other status (500)
//     is CodeUpstream.
//   - On HTTP 200, a "status":"error" envelope, an undecodable body and an unparsable
//     sample are CodeUpstream.
//   - A base URL that cannot form a request, and a range with a non-positive step or an
//     end before its start, are CodeInvalidInput with no send.
func TestClient_ErrorStatusIsCoded(t *testing.T) {
	t.Parallel()

	cases := testClientErrorStatusIsCodedCases()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testClientErrorStatusIsCodedCase(t, &tc)
		})
	}

	t.Run("bad base URL", func(t *testing.T) {
		t.Parallel()

		doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
		doer.EXPECT().Do(gomock.Any()).Times(0)

		_, err := promhttp.New("http://[::1", doer).Query(t.Context(), textUp)

		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})
	t.Run("bad range", func(t *testing.T) {
		t.Parallel()

		doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
		doer.EXPECT().Do(gomock.Any()).Times(0)

		currentClient := promhttp.New("http://prom.test:9090", doer)
		instant := time.Unix(1727260000, 0)

		_, err := currentClient.QueryRange(
			t.Context(),
			textUp,
			instant,
			instant.Add(time.Hour),
			0,
		)
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

		_, err = currentClient.QueryRange(
			t.Context(),
			textUp,
			instant.Add(time.Hour),
			instant,
			time.Minute,
		)
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})
}

// testClientErrorStatusIsCodedCase is the body of a closure in
// TestClient_ErrorStatusIsCoded.
func testClientErrorStatusIsCodedCase(
	t *testing.T,
	tcRef *testClientErrorStatusIsCodedRow,
) {
	t.Helper()

	testCase := *tcRef

	doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			if testCase.doErr != nil {
				return nil, testCase.doErr
			}

			return promResponse(testCase.status, testCase.body), nil
		})

	_, err := promhttp.New("http://prom.test:9090", doer).
		Query(t.Context(), textUp)

	require.Error(t, err)
	assert.Equal(t, testCase.wantCode, apperr.Code(err))
	assert.Contains(t, err.Error(), testCase.wantMsg)
}

// testClientErrorStatusIsCodedRow is one row of testClientErrorStatusIsCodedTable.
type testClientErrorStatusIsCodedRow struct {
	doErr    error
	body     string
	name     string
	wantMsg  string
	wantCode apperr.ErrorCode
	status   int
}

// testClientErrorStatusIsCodedCases builds the table testClientErrorStatusIsCoded runs.
func testClientErrorStatusIsCodedCases() []testClientErrorStatusIsCodedRow {
	return slices.Concat(
		testClientErrorStatusIsCodedClientSideCases(),
		testClientErrorStatusIsCodedServerSideCases(),
	)
}

// testClientErrorStatusIsCodedClientSideCases lists part of the
// testClientErrorStatusIsCodedCases rows.
func testClientErrorStatusIsCodedClientSideCases() []testClientErrorStatusIsCodedRow {
	return []testClientErrorStatusIsCodedRow{
		fixtures.Build(func(row *testClientErrorStatusIsCodedRow) {
			row.name = "transport"
			row.doErr = apperr.Sentinel("connection refused")
			row.wantCode = apperr.CodeUnavailable
		}),
		fixtures.Build(func(row *testClientErrorStatusIsCodedRow) {
			row.name = "caller canceled"
			row.doErr = &url.Error{
				Op:  textGetTitle,
				URL: "http://prom.test:9090",
				Err: context.Canceled,
			}
			row.wantCode = apperr.CodeCanceled
		}),
		fixtures.Build(func(row *testClientErrorStatusIsCodedRow) {
			row.name = "caller deadline"
			row.doErr = &url.Error{
				Op:  textGetTitle,
				URL: "http://prom.test:9090",
				Err: context.DeadlineExceeded,
			}
			row.wantCode = apperr.CodeTimeout
		}),
		{
			name:     "HTTP 400",
			status:   http.StatusBadRequest,
			body:     `{"status":"error","error":"parse error at char 3"}`,
			wantCode: apperr.CodeInvalidInput,
			wantMsg:  "parse error at char 3",
			doErr:    nil,
		},
		{
			name:     "HTTP 429",
			status:   http.StatusTooManyRequests,
			body:     ``,
			wantCode: apperr.CodeUnavailable,
			doErr:    nil,
			wantMsg:  "",
		},
		{
			name:     "HTTP 502 html",
			status:   http.StatusBadGateway,
			body:     `<html>`,
			wantCode: apperr.CodeUnavailable,
			doErr:    nil,
			wantMsg:  "",
		},
	}
}

// testClientErrorStatusIsCodedServerSideCases lists part of the
// testClientErrorStatusIsCodedCases rows.
func testClientErrorStatusIsCodedServerSideCases() []testClientErrorStatusIsCodedRow {
	return []testClientErrorStatusIsCodedRow{
		{
			name:     "HTTP 503",
			status:   http.StatusServiceUnavailable,
			body:     `{"status":"error","error":"starting up"}`,
			wantCode: apperr.CodeUnavailable,
			wantMsg:  "starting up",
			doErr:    nil,
		},
		{
			name:     "HTTP 500",
			status:   http.StatusInternalServerError,
			body:     `{}`,
			wantCode: apperr.CodeUpstream,
			doErr:    nil,
			wantMsg:  "",
		},
		{
			name:     "status error",
			status:   http.StatusOK,
			body:     `{"status":"error","error":"query timed out"}`,
			wantCode: apperr.CodeUpstream,
			wantMsg:  "query timed out",
			doErr:    nil,
		},
		{
			name:     "bad json",
			status:   http.StatusOK,
			body:     `<html>`,
			wantCode: apperr.CodeUpstream,
			doErr:    nil,
			wantMsg:  "",
		},
		{
			name:     "bad sample",
			status:   http.StatusOK,
			body:     strings.Replace(promVector, textSLower, promInstant("abc"), 1),
			wantCode: apperr.CodeUpstream,
			doErr:    nil,
			wantMsg:  "",
		},
	}
}

// TestPrometheusFromConfig_StubDefaultAndHTTPNeedsBaseURL tests the factory: the
// default config boots the stub with no infrastructure, and an incomplete http config
// fails loudly at construction.
//
// Why this test is important:
//   - The graph must boot with zero infrastructure, and an http kind with nowhere to
//     send queries must stop the process at startup.
//
// What it tests:
//   - DefaultConfig builds the stub: Query returns 0 and QueryRange an empty series, no
//     error.
//   - The http kind without a base URL is CodeInvalidInput.
//   - The http kind with a base URL builds a querier without sending anything.
func TestPrometheusFromConfig_StubDefaultAndHTTPNeedsBaseURL(t *testing.T) {
	t.Parallel()

	metricsQuerier, err := prometheus.NewFromConfig(
		t.Context(),
		prometheus.DefaultConfig(),
		stdx.Zero[prometheus.Deps](),
	)
	require.NoError(t, err)

	v, err := metricsQuerier.Query(t.Context(), textUp)
	require.NoError(t, err)
	assert.InDelta(t, wantV, v, 0)

	series, err := metricsQuerier.QueryRange(
		t.Context(),
		textUp,
		time.Unix(0, 0),
		time.Unix(unixInput, 0),
		time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, []promhttp.Sample{}, series)

	noURL := prometheus.DefaultConfig()

	noURL.Kind = prometheus.KindHTTP
	_, err = prometheus.NewFromConfig(t.Context(), noURL, stdx.Zero[prometheus.Deps]())
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	withURL := noURL

	withURL.HTTP.BaseURL = "http://prom.invalid:9090"
	metricsQuerier, err = prometheus.NewFromConfig(
		t.Context(),
		withURL,
		stdx.Zero[prometheus.Deps](),
	)
	require.NoError(t, err)
	assert.NotNil(t, metricsQuerier)
}

// TestDecorateDoer_RetriesTransportErrorsAndBuffersBody tests the client-stack wrapper
// around the HTTP transport.
//
// Why this test is important:
//   - The stack's per-attempt timeout ends when an attempt returns; a body read after
//     that would fail, and a transient transport error that is not retried fails a query
//     a second attempt would have answered.
//
// What it tests:
//   - With retries on, a transport error, then a 503, then a 200 returns the 200; the
//     transport body is closed inside the attempt, the returned body still reads in full,
//     and the attempts are counted under the operation named for the endpoint
//     (prometheus.query_range).
//   - With retries off, a transport error comes back CodeUnavailable, and a body over the
//     cap is CodeInvalidInput.
func TestDecorateDoer_RetriesTransportErrorsAndBuffersBody(t *testing.T) {
	t.Parallel()

	layers := new(decoration.DefaultConfig().Clients).For(textPrometheus)
	on := true

	layers.Retry.Enabled = &on
	layers.Retry.RetryConfig.InitialInterval = time.Millisecond
	layers.Retry.RetryConfig.MaxInterval = time.Millisecond

	reg := promclient.NewRegistry()
	deps, err := decorate.Build(
		textPrometheus,
		&layers,
		fixtures.Build(func(row *decorate.Collaborators) {
			row.Metrics = prom.NewFromRegistry(reg)
		}),
	)
	require.NoError(t, err)

	stack := wireBase(textPrometheus, &deps)
	ctrl := gomock.NewController(t)
	inner := mocks.NewMockPrometheusHTTPDoer(ctrl)
	// The transport body is closed by the wrapper before Do returns; the caller still
	// reads it.
	src := mocks.NewMockReadCloser(ctrl)
	src.EXPECT().Read(gomock.Any()).DoAndReturn(func(p []byte) (int, error) {
		return copy(p, "payload"), io.EOF
	})
	src.EXPECT().Close().Return(nil)

	var success http.Response

	success.StatusCode = http.StatusOK
	success.Body = src

	gomock.InOrder(
		inner.EXPECT().Do(gomock.Any()).Return(nil, apperr.Sentinel("connection reset")),
		inner.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(*http.Request) (*http.Response, error) {
				return promResponse(http.StatusServiceUnavailable, ``), nil
			}),
		inner.EXPECT().
			Do(gomock.Any()).
			Return(&success, nil),
	)

	testDecorateDoerRetriesTransportErrorsAndBuffersBodyBuffersBody(t, reg, &stack, inner)
}

// testDecorateDoerRetriesTransportErrorsAndBuffersBodyBuffersBody continues
// TestDecorateDoer_RetriesTransportErrorsAndBuffersBody: buffers body.
func testDecorateDoerRetriesTransportErrorsAndBuffersBodyBuffersBody(
	t *testing.T,
	reg *promclient.Registry,
	stack *client.Base,
	inner *mocks.MockPrometheusHTTPDoer,
) {
	t.Helper()

	var err error

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://prom.test/api/v1/query_range", http.NoBody)
	require.NoError(t, err)

	resp, err := promhttp.DecorateDoer(inner, stack, 1024).Do(req)

	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "payload", string(body))
	assert.InDelta(
		t,
		wantClientOpCounterBody,
		clientOpCounter(t, reg, "prometheus.query_range"),
		0,
	)

	bare := stdx.Zero[client.Base]()
	failing := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
	failing.EXPECT().Do(gomock.Any()).Return(nil, apperr.Sentinel("connection refused"))

	err = doClosed(promhttp.DecorateDoer(failing, &bare, 0), req)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	large := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
	large.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			return promResponse(http.StatusOK, strings.Repeat(textX, 9)), nil
		})

	err = doClosed(promhttp.DecorateDoer(large, &bare, decorateDoerInput), req)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

const (
	// decorateDoerInput is the maxBodyBytes passed to DecorateDoer in
	// testDecorateDoerRetriesTransportErrorsAndBuffersBodyBuffersBody.
	decorateDoerInput = 8

	// unixInput is the sec passed to Unix in
	// TestPrometheusFromConfig_StubDefaultAndHTTPNeedsBaseURL.
	unixInput = 60

	// valueMatrixTable is the Value field in testClientQueryRangeParsesMatrixTable.
	valueMatrixTable = 2

	// valueTable is the Value field in testClientQueryRangeParsesMatrixTable.
	valueTable = 1.5

	// wantClientOpCounterBody is the expected client op counter body.
	wantClientOpCounterBody = 3.0

	// wantTable is the expected table.
	wantTable = 12.5

	// wantV is the expected v.
	wantV = 0.0
)

const (
	// textNan is the literal "NaN".
	textNan = "NaN"
)
