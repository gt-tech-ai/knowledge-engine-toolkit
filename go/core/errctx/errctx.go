// Package errctx carries a request-scoped sink that captures the original
// domain error behind a sanitized transport error.
//
// The transport layer maps a domain error to a client-safe error (hiding SQL
// errors, stack traces, etc.) before returning it to the caller. That mapping
// discards the real cause, so a single request-logging interceptor could not
// otherwise record what actually failed. The sink bridges that gap: the mapper
// Captures the real error into a per-request sink, and the interceptor reads it
// back after the handler returns — one log line per request, with the true
// cause, without every layer logging its own copy.
package errctx

import "context"

// sinkKey is the unexported context key for the per-request error sink.
type sinkKey struct{}

// sink holds the real error captured for the current request. A pointer is
// stored in the context once, then mutated by Capture deeper in the call stack
// and read by the interceptor above it.
type sink struct {
	// err is the most recent real error captured for the request.
	err error
}

// WithSink returns a context carrying a fresh error sink. The request-logging
// interceptor installs one at the start of every request.
func WithSink(ctx context.Context) context.Context {
	return context.WithValue(ctx, sinkKey{}, &sink{})
}

// Capture records err as the real error for the current request when a sink is
// present. It is a no-op if no sink was installed, so callers need not know
// whether they run inside a request.
func Capture(ctx context.Context, err error) {
	if s, ok := ctx.Value(sinkKey{}).(*sink); ok {
		s.err = err
	}
}

// Captured returns the real error recorded for the current request, or nil if
// none was captured (or no sink is installed).
func Captured(ctx context.Context) error {
	if s, ok := ctx.Value(sinkKey{}).(*sink); ok {
		return s.err
	}
	return nil
}
