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
	"strconv"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "build ping request")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeUnavailable, "ping "+u)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return apperr.New(apperr.CodeUnavailable, "obs backend not ready: "+u)
		}
	}
	return nil
}

// TraceIDFromHeader extracts the 32-hex trace id from a W3C traceparent-format
// `traceresponse` header (`00-<trace-id>-<span-id>-<flags>`), or "" when the header is
// absent or malformed.
func TraceIDFromHeader(traceresponse string) string {
	parts := strings.Split(traceresponse, "-")
	if len(parts) < 2 || len(parts[1]) != 32 {
		return ""
	}
	for _, r := range parts[1] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return parts[1]
}

// ForceSampledTraceparent builds a W3C `traceparent` header value (`00-<32hex
// trace>-<16hex span>-01`) with the sampled trace-flag (`01`) set, plus the bare 32-hex
// trace id it carries. An endpoint-E2E client sends this header so the server's
// ParentBased sampler (foundation/tracer) force-samples THIS request — the lever that
// guarantees the request's trace reaches Tempo without raising the sample ratio for
// normal traffic. The ids are crypto-random so concurrent runs never collide.
func ForceSampledTraceparent() (header, traceID string, err error) {
	var tid [16]byte
	var sid [8]byte
	if _, err := rand.Read(tid[:]); err != nil {
		return "", "", apperr.Wrap(err, apperr.CodeInternal, "e2ekit: random trace id")
	}
	if _, err := rand.Read(sid[:]); err != nil {
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
	if err := c.getJSON(
		ctx,
		c.tempoURL+"/api/traces/"+url.PathEscape(traceID),
		&body,
	); err != nil {
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
	var body struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
		} `json:"batches"`
	}
	if err := c.getJSON(
		ctx,
		c.tempoURL+"/api/traces/"+url.PathEscape(traceID),
		&body,
	); err != nil {
		return false, err
	}
	for _, batch := range body.Batches {
		for _, attr := range batch.Resource.Attributes {
			if attr.Key == "service.name" && attr.Value.StringValue == service {
				return true, nil
			}
		}
	}
	return false, nil
}

// MetricValue returns the first instant-vector sample value for promQL, or 0 (nil error)
// when the vector is empty (metric not yet scraped). Callers poll on 0.
func (c *TelemetryClient) MetricValue(
	ctx context.Context,
	promQL string,
) (float64, error) {
	endpoint := c.promURL + "/api/v1/query?query=" + url.QueryEscape(promQL)
	var body struct {
		Data struct {
			Result []struct {
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, &body); err != nil {
		return 0, err
	}
	if len(body.Data.Result) == 0 {
		return 0, nil
	}
	// A Prometheus sample is [<unix-ts>, "<number>"]: the value is the string second
	// element.
	var sample string
	if err := json.Unmarshal(body.Data.Result[0].Value[1], &sample); err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "parse prometheus sample")
	}
	value, err := strconv.ParseFloat(sample, 64)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "parse prometheus value")
	}
	return value, nil
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
		Data struct {
			Result []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, &body); err != nil {
		return 0, err
	}
	lines := 0
	for _, stream := range body.Data.Result {
		lines += len(stream.Values)
	}
	return lines, nil
}

// PollTraceInTempo polls ta until traceID has at least one span batch in Tempo, or ~20s
// elapse (spans reach Tempo a few seconds after the RPC). Returns nil on success, else a
// CodeUnavailable error carrying the last error/miss.
func PollTraceInTempo(ctx context.Context, ta TraceAsserter, traceID string) error {
	return pollEvery(
		ctx,
		20*time.Second,
		500*time.Millisecond,
		"trace "+traceID+" in Tempo",
		func(ctx context.Context) (bool, error) {
			n, err := ta.TraceInTempo(ctx, traceID)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apperr.New(apperr.CodeInternal, "telemetry query failed: "+resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "decode telemetry response")
	}
	return nil
}
