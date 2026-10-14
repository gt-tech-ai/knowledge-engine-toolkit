// Package http is the HTTP backend of the Prometheus querier: instant queries reduced
// to one sample, and range queries reduced to one series, over a Prometheus server's
// HTTP query API. Both fail closed: a result that is not exactly one finite value (or
// one series of them) is an error, never a silent zero.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// HTTPDoer is the HTTP boundary the client sends its requests through.
//
// SDK seam — matches *nethttp.Client's Do, the stdlib HTTP transport boundary; it
// types the wire call, not a port, so it cannot compose with a core interface.
type HTTPDoer interface {
	// Do sends req and returns its response (the caller closes its body).
	Do(req *nethttp.Request) (*nethttp.Response, error)
}

// Config configures the HTTP backend.
type Config struct {
	// BaseURL is the Prometheus server root (e.g. its in-cluster service address);
	// required.
	BaseURL string `yaml:"base_url" mapstructure:"base_url"`

	// Timeout is the http.Client timeout: it bounds each HTTP attempt, response body
	// included, even with the client resilience disabled; zero means no transport
	// bound.
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`

	// MaxBodyBytes caps a response body the backend buffers; a larger one is
	// CodeInvalidInput. Zero or less means no cap.
	MaxBodyBytes int64 `yaml:"max_body_bytes" mapstructure:"max_body_bytes"`
}

// Sample is one point of a range-query series.
type Sample = types.MetricSample

// Client queries the Prometheus server at its base URL.
type Client struct {
	// doer sends the HTTP requests.
	doer HTTPDoer

	// baseURL is the Prometheus server root, without a trailing slash.
	baseURL string
}

// Client satisfies the metrics-query port.
var _ interfaces.MetricsQuerier = (*Client)(nil)

// New builds the client for the Prometheus server at baseURL (e.g. the in-cluster
// service address), sending requests through doer.
func New(baseURL string, doer HTTPDoer) *Client {
	return &Client{doer: doer, baseURL: strings.TrimRight(baseURL, "/")}
}

// response is the Prometheus query-API response envelope.
type response struct {
	// Status is "success" or "error".
	Status string `json:"status"`

	// Error is the server's message when Status is "error".
	Error string `json:"error"`

	// Data holds the result.
	Data struct {
		// ResultType is "vector" for an instant query, "matrix" for a range query.
		ResultType string `json:"resultType"`

		// Result holds one entry per series.
		Result []struct {
			// Value is an instant vector's [<unix time>, "<value>"] sample.
			Value [2]json.RawMessage `json:"value"`

			// Values are a matrix series' [<unix time>, "<value>"] samples.
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// Query evaluates promql as an instant query and returns its single sample value. No
// sample, several series, or a NaN or infinite value is an error: a value that can't be
// measured is never reported as 0.
func (c *Client) Query(ctx context.Context, promql string) (float64, error) {
	body, err := c.get(ctx, "/api/v1/query", url.Values{"query": {promql}})
	if err != nil {
		return 0, err
	}
	if body.Data.ResultType != "vector" {
		return 0, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"query returned a %q, want an instant vector", body.Data.ResultType,
		))
	}
	switch n := len(body.Data.Result); {
	case n == 0:
		return 0, apperr.New(apperr.CodeNotFound, "query returned no sample (no data)")
	case n > 1:
		return 0, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"query returned %d series, want one (aggregate it, e.g. with sum or max)", n,
		))
	}
	s, err := parseSample(body.Data.Result[0].Value)
	if err != nil {
		return 0, err
	}
	return s.Value, nil
}

// QueryRange evaluates promql over [start, end] at step and returns its single series.
// No series is an empty series (nothing was recorded in the window); several series or
// a NaN or infinite sample is an error.
func (c *Client) QueryRange(
	ctx context.Context,
	promql string,
	start, end time.Time,
	step time.Duration,
) ([]Sample, error) {
	if step <= 0 || end.Before(start) {
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"range query needs a positive step and end >= start (step %s, %s..%s)",
			step, start.Format(time.RFC3339), end.Format(time.RFC3339),
		))
	}
	body, err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	})
	if err != nil {
		return nil, err
	}
	if body.Data.ResultType != "matrix" {
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"query returned a %q, want a range matrix", body.Data.ResultType,
		))
	}
	switch n := len(body.Data.Result); {
	case n == 0:
		return []Sample{}, nil
	case n > 1:
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"query returned %d series, want one (aggregate it, e.g. with sum)", n,
		))
	}
	raw := body.Data.Result[0].Values
	out := make([]Sample, 0, len(raw))
	for _, pair := range raw {
		s, err := parseSample(pair)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// get sends a GET of path with params and decodes a successful response envelope.
func (c *Client) get(
	ctx context.Context,
	path string,
	params url.Values,
) (response, error) {
	var body response
	endpoint := c.baseURL + path + "?" + params.Encode()
	req, err := nethttp.NewRequestWithContext(
		ctx,
		nethttp.MethodGet,
		endpoint,
		nethttp.NoBody,
	)
	if err != nil {
		return body, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"build prometheus query request",
		)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return body, transportError(err, "query prometheus")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != nethttp.StatusOK {
		return body, statusError(resp.StatusCode, resp.Body)
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return body, apperr.Wrap(err, apperr.CodeUpstream, "decode prometheus response")
	}
	if body.Status != "success" {
		return body, apperr.New(
			apperr.CodeUpstream,
			"prometheus query failed: "+body.Error,
		)
	}
	return body, nil
}

// transportError codes a failed send. A code the doer already chose survives; otherwise
// the caller's own deadline is CodeTimeout, its cancellation CodeCanceled, and any other
// failure CodeUnavailable.
func transportError(err error, msg string) error {
	code := apperr.Code(err)
	switch {
	case code != apperr.CodeUnknown:
	case apperr.StdIs(err, context.DeadlineExceeded):
		code = apperr.CodeTimeout
	case apperr.StdIs(err, context.Canceled):
		code = apperr.CodeCanceled
	default:
		code = apperr.CodeUnavailable
	}
	return apperr.Wrap(err, code, msg)
}

// isTransientStatus reports whether an HTTP status means the server is throttling or
// briefly unavailable, so a later attempt may succeed.
func isTransientStatus(status int) bool {
	switch status {
	case nethttp.StatusTooManyRequests, nethttp.StatusBadGateway,
		nethttp.StatusServiceUnavailable, nethttp.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// statusError codes an HTTP error status, quoting the server's error text when body is a
// query-API envelope. A transient status (see isTransientStatus) is CodeUnavailable, so
// the client stack retries it and its breaker counts it. A rejected query (400, 422) is
// CodeInvalidInput. Any other status is CodeUpstream.
func statusError(status int, body io.Reader) error {
	msg := fmt.Sprintf("prometheus query failed (HTTP %d)", status)
	var env response
	if json.NewDecoder(body).Decode(&env) == nil && env.Error != "" {
		msg += ": " + env.Error
	}
	switch {
	case isTransientStatus(status):
		return apperr.New(apperr.CodeUnavailable, msg)
	case status == nethttp.StatusBadRequest, status == nethttp.StatusUnprocessableEntity:
		return apperr.New(apperr.CodeInvalidInput, msg)
	default:
		return apperr.New(apperr.CodeUpstream, msg)
	}
}

// parseSample decodes one [<unix time>, "<value>"] pair, failing closed on a NaN or
// infinite value.
func parseSample(pair [2]json.RawMessage) (Sample, error) {
	var ts float64
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return Sample{}, apperr.Wrap(
			err,
			apperr.CodeUpstream,
			"parse prometheus sample time",
		)
	}
	var raw string
	if err := json.Unmarshal(pair[1], &raw); err != nil {
		return Sample{}, apperr.Wrap(
			err,
			apperr.CodeUpstream,
			"parse prometheus sample",
		)
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return Sample{}, apperr.Wrap(
			err,
			apperr.CodeUpstream,
			"parse prometheus sample value",
		)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Sample{}, apperr.New(
			apperr.CodeNotFound,
			"query returned "+raw+" (no data)",
		)
	}
	sec, frac := math.Modf(ts)
	return Sample{Time: time.Unix(int64(sec), int64(frac*1e9)).UTC(), Value: v}, nil
}
