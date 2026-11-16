package logctx

import (
	"context"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

// maxStackDepth is the most frames a captured stack holds.
const maxStackDepth = 64

// captureStackSkip is the frames dropped: runtime.Callers and captureStack.
const captureStackSkip = 2

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

	// flatten, set under SchemaV2, rewrites a logged error into its message and
	// error_code after the origin stack has been read from it.
	flatten bool
}

// NewStacktraceHandler returns a handler that appends a captured stack trace to
// records logged at or above minLevel. Records below the threshold pass through
// unchanged.
func NewStacktraceHandler(inner slog.Handler, minLevel slog.Level) slog.Handler {
	return &stacktraceHandler{inner: inner, level: minLevel, flatten: false}
}

// NewSchemaV2StacktraceHandler returns NewStacktraceHandler's handler that also
// flattens an error logged under ErrorKey, on every record and every WithAttrs,
// to its message string beside its ErrorCodeKey (ARCHITECTURE.md#logging). The
// stack is read from the error before it is flattened, so an AppError still
// logs its origin stack.
func NewSchemaV2StacktraceHandler(inner slog.Handler, minLevel slog.Level) slog.Handler {
	return &stacktraceHandler{inner: inner, level: minLevel, flatten: true}
}

// Enabled reports whether the handler processes records at the given level by
// delegating to the wrapped handler.
func (h *stacktraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle attaches a stack trace to records at or above the configured level
// before forwarding them to the wrapped handler. It prefers an origin stack
// carried by a logged error and falls back to capturing the current stack.
func (h *stacktraceHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level < h.level {
		return h.inner.Handle(ctx, h.shape(&record))
	}

	// Prefer an origin stack carried by a logged error (e.g. an AppError) so the
	// trace points at where the error was created, not this logging call site.
	// Fall back to capturing the current stack when no error supplies one.
	stack := originStack(&record)
	if stack == "" {
		stack = captureStack()
	}

	secondR := h.shape(&record)
	secondR.AddAttrs(slog.String(StacktraceKey, stack))

	return h.inner.Handle(ctx, secondR)
}

// stackTracer is implemented by errors that carry an origin stack trace
// (notably core/apperr.AppError). It is matched structurally so this package
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
func originStack(r *slog.Record) string {
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
// attributes added (each logged error flattened under flatten), preserving the
// stack-trace threshold.
func (h *stacktraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h.flatten {
		attrs = flattenErrors(attrs)
	}

	return &stacktraceHandler{
		inner:   h.inner.WithAttrs(attrs),
		level:   h.level,
		flatten: h.flatten,
	}
}

// WithGroup returns a new handler whose wrapped handler opens the named group,
// preserving the stack-trace threshold.
func (h *stacktraceHandler) WithGroup(name string) slog.Handler {
	return &stacktraceHandler{
		inner:   h.inner.WithGroup(name),
		level:   h.level,
		flatten: h.flatten,
	}
}

// captureStack walks the call stack and returns a newline-separated
// "func\n\tfile:line" trace, skipping runtime, log/slog, and logger-internal
// frames so the trace starts at the caller's own code.
func captureStack() string {
	pcs := make([]uintptr, maxStackDepth)
	// Skip runtime.Callers + captureStack itself.
	n := runtime.Callers(captureStackSkip, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	var builder strings.Builder

	for {
		frame, more := frames.Next()
		// Drop logging plumbing frames wherever they appear so the trace shows
		// only application code.
		if !isInternalFrame(frame.Function, frame.File) {
			_, _ = builder.WriteString(frame.Function)
			_, _ = builder.WriteString("\n\t")
			_, _ = builder.WriteString(frame.File)
			_ = builder.WriteByte(':')
			_, _ = builder.WriteString(strconv.Itoa(frame.Line))
			_ = builder.WriteByte('\n')
		}

		if !more {
			break
		}
	}

	return builder.String()
}

// isInternalFrame reports whether a frame belongs to the logging plumbing and
// should be skipped at the head of the trace.
func isInternalFrame(fn, file string) bool {
	return strings.Contains(file, "/log/slog/") ||
		strings.Contains(fn, "log/slog") ||
		strings.Contains(file, "foundation/logger/") ||
		strings.Contains(fn, "go.uber.org/zap")
}

// shape returns a copy of record the wrapped handler may extend: a clone, or under
// flatten a rebuild with every logged error flattened.
func (h *stacktraceHandler) shape(record *slog.Record) slog.Record {
	if !h.flatten {
		return record.Clone()
	}

	out := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)

	record.Attrs(func(attr slog.Attr) bool {
		out.AddAttrs(flattenError(attr)...)

		return true
	})

	return out
}
