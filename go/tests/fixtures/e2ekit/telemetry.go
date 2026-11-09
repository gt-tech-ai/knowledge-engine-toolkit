// Package e2ekit is the gen-free shared test-kit for endpoint E2E + telemetry suites. It
// holds the telemetry assertions that prove a request's spans/metrics/logs landed in
// Tempo/Prometheus/Loki (correlated by the trace id carried back in the `traceresponse`
// header) and PollUntil, the primitive for asserting an async effect. It imports NO
// generated code, so both a CLI endpoint harness and a service's `//go:build e2e` tests
// (which own the generated Connect clients) can share it without dragging generated proto
// into a bootstrap CLI.
package e2ekit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// traceWaitTimeout bounds the wait for a trace to reach the backend.
const traceWaitTimeout = 20 * time.Second

// tracePollInterval is the wait between trace lookups.
const tracePollInterval = 500 * time.Millisecond

// HTTPDoer is the minimal HTTP boundary the telemetry assertions need. *http.Client
// satisfies it; a unit test injects a mock so query construction + response parsing are
// testable without live backends.
//
// interface-composition exemption — consumer-side seam (accept interfaces): the stdlib
// http client boundary, no core interface to embed.
type HTTPDoer interface {
	// Do executes an HTTP request and returns its response.
	Do(req *http.Request) (*http.Response, error)
}

// TraceAsserter is the minimal telemetry seam a case needs to prove a request's trace
// landed in Tempo, correlated by the trace id from the response's traceresponse header.
// *TelemetryClient satisfies it; a unit test injects a stub so the case is testable
// without Tempo.
//
// interface-composition exemption — consumer-side seam (accept interfaces): a one-method
// Tempo boundary, no core interface to embed.
type TraceAsserter interface {
	// TraceInTempo returns the number of resource-span batches Tempo holds for traceID (0
	// if absent).
	TraceInTempo(ctx context.Context, traceID string) (int, error)
}

// TelemetryClient asserts that a request's telemetry landed in the live backends,
// correlated by trace id. Backend base URLs are injected (config DATA; default to the dev
// compose host ports).
type TelemetryClient struct {
	http     HTTPDoer
	tempoURL string
	promURL  string
	lokiURL  string
}

// NewTelemetryClient builds a TelemetryClient over the given HTTP doer and backend base
// URLs (e.g. "http://localhost:3200", ":9090", ":3100").
func NewTelemetryClient(
	doer HTTPDoer,
	tempoURL, promURL, lokiURL string,
) *TelemetryClient {
	return &TelemetryClient{
		http:     doer,
		tempoURL: tempoURL,
		promURL:  promURL,
		lokiURL:  lokiURL,
	}
}

// Ping reports whether all three observability backends are reachable, satisfying the
// harness Pinger so the telemetry precheck can skip-with-reason when the obs stack is
// down. It probes each backend's readiness path and returns the first failure.
func (c *TelemetryClient) Ping(ctx context.Context) error {
	for _, u := range []string{
		c.tempoURL + "/ready",
		c.promURL + "/-/healthy",
		c.lokiURL + "/ready",
	} {
		err := c.pingOne(ctx, u)
		if err != nil {
			return err
		}
	}

	return nil
}

// TraceIDFromHeader extracts the 32-hex trace id from a W3C traceparent-format
// `traceresponse` header (`00-<trace-id>-<span-id>-<flags>`), or "" when the header is
// absent or malformed.
func TraceIDFromHeader(traceresponse string) string {
	parts := strings.Split(traceresponse, "-")

	if len(parts) < partsLen2 || len(parts[1]) != partsLen32 {
		return ""
	}

	for _, r := range parts[1] {
		if !isLowerHexRune(r) {
			return ""
		}
	}

	return parts[1]
}

// isLowerHexRune reports a digit or a lowercase hex letter.
func isLowerHexRune(
	r rune,
) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// ForceSampledTraceparent builds a W3C `traceparent` header value (`00-<32hex
// trace>-<16hex span>-01`) with the sampled trace-flag (`01`) set, plus the bare 32-hex
// trace id it carries. An endpoint-E2E client sends this header so the server's
// ParentBased sampler (foundation/tracer) force-samples THIS request — the lever that
// guarantees the request's trace reaches Tempo without raising the sample ratio for
// normal traffic. The ids are crypto-random so concurrent runs never collide.
func ForceSampledTraceparent() (header, traceID string, err error) {
	var (
		tid [16]byte
		sid [8]byte
	)

	_, err = rand.Read(tid[:])
	if err != nil {
		return "", "", apperr.Wrap(err, apperr.CodeInternal, "e2ekit: random trace id")
	}

	_, err = rand.Read(sid[:])
	if err != nil {
		return "", "", apperr.Wrap(err, apperr.CodeInternal, "e2ekit: random span id")
	}

	traceID = hex.EncodeToString(tid[:])

	return "00-" + traceID + "-" + hex.EncodeToString(sid[:]) + "-01", traceID, nil
}

// TraceInTempo returns the number of resource-span batches Tempo holds for traceID, or 0
// (nil error) when the trace is not yet present. Callers poll on 0 (spans reach Tempo a
// few seconds after the RPC).
func (c *TelemetryClient) TraceInTempo(ctx context.Context, traceID string) (int, error) {
	var body struct {
		Batches []json.RawMessage `json:"batches"`
	}

	err := c.getJSON(
		ctx,
		c.tempoURL+"/api/traces/"+url.PathEscape(traceID),
		&body,
	)
	if err != nil {
		return 0, err
	}

	return len(body.Batches), nil
}

// TraceSpansService returns true when Tempo's trace for traceID contains a resource batch
// whose `service.name` resource attribute equals service — the model-independent proof
// that the trace actually reached that service (e.g. a trace that crossed into a
// downstream worker), not merely that SOME spans exist. Returns false (nil error) when
// the trace is absent or names no such service, so callers poll on false.
func (c *TelemetryClient) TraceSpansService(
	ctx context.Context,
	traceID, service string,
) (bool, error) {
	var body tempoTrace

	err := c.getJSON(
		ctx,
		c.tempoURL+"/api/traces/"+url.PathEscape(traceID),
		&body,
	)
	if err != nil {
		return false, err
	}

	return body.namesService(service), nil
}

// tempoAttributeValue is the Value element of tempoAttribute.
type tempoAttributeValue struct {
	StringValue string `json:"stringValue"`
}

// tempoAttribute is one resource attribute of a Tempo batch.
type tempoAttribute struct {
	Key   string              `json:"key"`
	Value tempoAttributeValue `json:"value"`
}

// tempoBatchResource is the Resource element of tempoBatch.
type tempoBatchResource struct {
	Attributes []tempoAttribute `json:"attributes"`
}

// tempoBatch is one resource batch of a Tempo trace.
type tempoBatch struct {
	Resource tempoBatchResource `json:"resource"`
}

// tempoTrace is the part of Tempo's trace response that names the services.
type tempoTrace struct {
	Batches []tempoBatch `json:"batches"`
}

// namesService reports whether a batch's service.name equals service.
func (t *tempoTrace) namesService(service string) bool {
	for _, batch := range t.Batches {
		if slices.ContainsFunc(batch.Resource.Attributes, func(a tempoAttribute) bool {
			return a.Key == "service.name" && a.Value.StringValue == service
		}) {
			return true
		}
	}

	return false
}

// shapeResult2 is the Result element of this document.
type shapeResult2 struct {
	Value [2]json.RawMessage `json:"value"`
}

// shapeData2 is the Data element of this document.
type shapeData2 struct {
	Result []shapeResult2 `json:"result"`
}

// MetricValue returns the first instant-vector sample value for promQL, or 0 (nil error)
// when the vector is empty (metric not yet scraped). Callers poll on 0.
func (c *TelemetryClient) MetricValue(
	ctx context.Context,
	promQL string,
) (float64, error) {
	endpoint := c.promURL + "/api/v1/query?query=" + url.QueryEscape(promQL)

	var body struct {
		Data shapeData2 `json:"data"`
	}

	err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return 0, err
	}

	if len(body.Data.Result) == 0 {
		return 0, nil
	}

	// A Prometheus sample is [<unix-ts>, "<number>"]: the value is the string second
	// element.
	var sample string

	err = json.Unmarshal(body.Data.Result[0].Value[1], &sample)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "parse prometheus sample")
	}

	value, err := strconv.ParseFloat(sample, bitSize64)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "parse prometheus value")
	}

	return value, nil
}

// shapeResult is the Result element of this document.
type shapeResult struct {
	Values [][]json.RawMessage `json:"values"`
}

// shapeData is the Data element of this document.
type shapeData struct {
	Result []shapeResult `json:"result"`
}

// LogHasTrace returns the number of Loki log lines matching selector that carry traceID,
// or 0 (nil error) when none yet. Callers poll on 0.
func (c *TelemetryClient) LogHasTrace(
	ctx context.Context,
	selector, traceID string,
) (int, error) {
	query := selector + " |= `" + traceID + "`"
	endpoint := c.lokiURL + "/loki/api/v1/query_range?query=" + url.QueryEscape(query)

	var body struct {
		Data shapeData `json:"data"`
	}

	err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return 0, err
	}

	lines := 0
	for _, stream := range body.Data.Result {
		lines += len(stream.Values)
	}

	return lines, nil
}

// pingOne requires one backend's health endpoint to answer 2xx.
func (c *TelemetryClient) pingOne(ctx context.Context, text string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, text, http.NoBody)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "build ping request")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "ping "+text)
	}

	_ = resp.Body.Close()
	if resp.StatusCode < statusCode200 || resp.StatusCode >= statusCode300 {
		return apperr.New(apperr.CodeUnavailable, "obs backend not ready: "+text)
	}

	return nil
}

// PollTraceInTempo polls ta until traceID has at least one span batch in Tempo, or ~20s
// elapse (spans reach Tempo a few seconds after the RPC). Returns nil on success, else a
// CodeUnavailable error carrying the last error/miss.
func PollTraceInTempo(
	ctx context.Context,
	traceAsserter TraceAsserter,
	traceID string,
) error {
	return pollEvery(
		ctx,
		traceWaitTimeout,
		tracePollInterval,
		"trace "+traceID+" in Tempo",
		func(ctx context.Context) (bool, error) {
			n, err := traceAsserter.TraceInTempo(ctx, traceID)

			return err == nil && n > 0, err
		},
	)
}

// getJSON GETs endpoint and decodes a 2xx body into out. A 404 is treated as "not found"
// (out is left zero-valued, no error) so the not-yet-present poll case is a zero result,
// not a failure; any other non-2xx is an error.
func (c *TelemetryClient) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "build telemetry request")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "telemetry request")
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode < statusCode200 || resp.StatusCode >= statusCode300 {
		return apperr.New(apperr.CodeInternal, "telemetry query failed: "+resp.Status)
	}

	err = json.NewDecoder(resp.Body).Decode(out)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "decode telemetry response")
	}

	return nil
}
