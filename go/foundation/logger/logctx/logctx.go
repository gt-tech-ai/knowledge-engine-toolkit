// Package logctx provides shared context utilities for logger sub-packages.
//
// This package exists to avoid import cycles between the parent logger package
// and its sub-packages (zap, stdlib). Both the parent and sub-packages import
// this package for context key definitions and PII redaction.
package logctx

import (
	"context"

	"github.com/grafana/regexp"
	"go.opentelemetry.io/otel/trace"
)

// contextKey is a private type for context value keys, preventing collisions
// with keys defined in other packages.
type contextKey string

// correlationIDKey is the context key under which the request correlation ID is
// stored and retrieved.
const correlationIDKey contextKey = "correlation_id"

// WithCorrelationID returns a context with the given correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID extracts the correlation ID from context. Returns empty string
// if no correlation ID is present.
func CorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok {
		return id
	}
	return ""
}

// TraceID extracts the OpenTelemetry trace ID from context. Returns the hex-encoded
// trace ID string if a valid span context is present, empty string otherwise.
// This enables log-trace correlation in Grafana (Loki → Tempo linking).
func TraceID(ctx context.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}

// SpanID extracts the OpenTelemetry span ID from context. Returns the hex-encoded
// span ID string if a valid span context is present, empty string otherwise.
// This enables drill-down from log entries to specific spans within a trace.
func SpanID(ctx context.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.SpanID().String()
}

// piiPattern is the single alternation regexp applied by RedactPII: one
// pass over the input replacing any email / phone / SSN / IPv4 / auth-token match
// with "[REDACTED]", instead of five sequential ReplaceAllString passes. The
// alternatives keep the previous order (email first, token last); RE2 is
// leftmost-first, so for the realistic, non-overlapping PII that appears in real
// log lines the output is identical to the previous per-pattern application (the
// comparison test pins this). No cheap @/digit pre-check is applied: a digit-less
// bearer token would be skipped by it — a redaction miss we won't risk.
//
// The auth-token alternative matches `bearer <t>`, `token[=:] <t>`, and
// `access_token[=:] <t>` — the last explicitly (not by relying on `token=` being a
// substring of `access_token=`) so the WebSocket auth token (`?access_token=<jwt>`)
// is fully redacted, including its `access_` prefix, wherever a URL is
// logged. A gateway's own access log never passes through this Go redactor, so
// scrub it in the log pipeline too.
//
// Compiled with grafana/regexp, a pure-Go, RE2-compatible drop-in that is faster
// on these small inputs (go-re2 regresses small inputs and is deliberately unused).
var piiPattern = regexp.MustCompile(
	`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}` + // email
		`|(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}` + // phone
		`|\b\d{3}-\d{2}-\d{4}\b` + // SSN
		`|\b(?:\d{1,3}\.){3}\d{1,3}\b` + // IPv4
		// auth token (incl. the access_token= WS query param)
		`|(?i:(?:bearer\s+|(?:access_)?token[=:]\s*)[a-zA-Z0-9\-._~+/]+=*)`,
)

// RedactPII replaces PII (email, phone, SSN, IPv4, auth tokens) with
// "[REDACTED]" in a single pass.
func RedactPII(s string) string {
	return piiPattern.ReplaceAllString(s, "[REDACTED]")
}
