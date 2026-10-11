package logctx

import (
	"context"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

// StacktraceKey is the attribute key under which the captured stack is logged.
const StacktraceKey = "stacktrace"

// stacktraceHandler wraps an slog.Handler and attaches a "stacktrace" attribute
// to every record at or above a threshold level (typically Error). This ensures
// the structured logger always emits a full stack trace for errors, matching the
// behaviour of the panic-recovery handlers.
type stacktraceHandler struct {
	// inner is the delegate handler that receives records after the stack
	// attribute has (or has not) been appended.
	inner slog.Handler

	// level is the threshold at or above which a stack trace is attached;
	// records below it pass through to inner unchanged.
	level slog.Level
}

// NewStacktraceHandler returns a handler that appends a captured stack trace to
// records logged at or above minLevel. Records below the threshold pass through
// unchanged.
func NewStacktraceHandler(inner slog.Handler, minLevel slog.Level) slog.Handler {
	return &stacktraceHandler{inner: inner, level: minLevel}
}

// Enabled reports whether the handler processes records at the given level by
// delegating to the wrapped handler.
func (h *stacktraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle attaches a stack trace to records at or above the configured level
// before forwarding them to the wrapped handler. It prefers an origin stack
// carried by a logged error and falls back to capturing the current stack.
func (h *stacktraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.level {
		return h.inner.Handle(ctx, r)
	}
	// Prefer an origin stack carried by a logged error (e.g. an AppError) so the
	// trace points at where the error was created, not this logging call site.
	// Fall back to capturing the current stack when no error supplies one.
	stack := originStack(r)
	if stack == "" {
		stack = captureStack()
	}
	r2 := r.Clone()
	r2.AddAttrs(slog.String(StacktraceKey, stack))
	return h.inner.Handle(ctx, r2)
}

// stackTracer is implemented by errors that carry an origin stack trace
// (notably core/errors.AppError). It is matched structurally so this package
// stays free of a dependency on the errors package.
//
// SDK seam — mirrors pkg/errors' unexported stackTracer interface (StackTrace() string),
// matched structurally to stay dependency-free, so it cannot compose with a core port.
type stackTracer interface {
	// StackTrace returns the captured stack from where the error originated.
	StackTrace() string
}

// originStack returns the first non-empty StackTrace() from a logged error
// attribute, or "" when no attribute supplies one.
func originStack(r slog.Record) string {
	var stack string
	r.Attrs(func(a slog.Attr) bool {
		if st, ok := a.Value.Resolve().Any().(stackTracer); ok {
			if s := st.StackTrace(); s != "" {
				stack = s
				return false
			}
		}
		return true
	})
	return stack
}

// WithAttrs returns a new handler whose wrapped handler has the given
// attributes added, preserving the stack-trace threshold.
func (h *stacktraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &stacktraceHandler{inner: h.inner.WithAttrs(attrs), level: h.level}
}

// WithGroup returns a new handler whose wrapped handler opens the named group,
// preserving the stack-trace threshold.
func (h *stacktraceHandler) WithGroup(name string) slog.Handler {
	return &stacktraceHandler{inner: h.inner.WithGroup(name), level: h.level}
}

// captureStack walks the call stack and returns a newline-separated
// "func\n\tfile:line" trace, skipping runtime, log/slog, and logger-internal
// frames so the trace starts at the caller's own code.
func captureStack() string {
	pcs := make([]uintptr, 64)
	// Skip runtime.Callers + captureStack itself.
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	var b strings.Builder
	for {
		frame, more := frames.Next()
		// Drop logging plumbing frames wherever they appear so the trace shows
		// only application code.
		if !isInternalFrame(frame.Function, frame.File) {
			b.WriteString(frame.Function)
			b.WriteString("\n\t")
			b.WriteString(frame.File)
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(frame.Line))
			b.WriteByte('\n')
		}
		if !more {
			break
		}
	}
	return b.String()
}

// isInternalFrame reports whether a frame belongs to the logging plumbing and
// should be skipped at the head of the trace.
func isInternalFrame(fn, file string) bool {
	return strings.Contains(file, "/log/slog/") ||
		strings.Contains(fn, "log/slog") ||
		strings.Contains(file, "foundation/logger/") ||
		strings.Contains(fn, "go.uber.org/zap")
}
