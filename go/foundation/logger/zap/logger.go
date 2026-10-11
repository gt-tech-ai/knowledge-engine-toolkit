// Package zap provides a zap-backed logger implementation with PII redaction.
package zap

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
)

// Compile-time interface assertion.
var _ interfaces.Logger = (*Logger)(nil)

// Config holds zap-specific logger configuration.
type Config struct {
	// Level sets the minimum log severity (debug, info, warn, error).
	Level string

	// Format selects the output encoding: "json" for structured or "text" for console.
	Format string

	// RedactPII enables automatic scrubbing of emails, phone numbers, SSNs, IPs, and tokens.
	RedactPII bool
}

// DefaultConfig returns zap-specific configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Level:     "info",
		Format:    "json",
		RedactPII: true,
	}
}

// Logger implements interfaces.Logger backed by zap via slog.
type Logger struct {
	// inner is the slog.Logger that performs the actual structured logging.
	inner *slog.Logger
}

// New creates a zap-backed logger from the given config.
func New(cfg Config) *Logger {
	l := newSlog(cfg)
	// Bind the deployed commit (GIT_SHA env = image tag) so every log line carries
	// git_sha, letting Grafana deep-link errors to GitHub source at that revision.
	// This is the one sanctioned bootstrap env read in the logger: it is a deploy-time
	// image constant read at logger construction — a deliberate exception to reading
	// configuration in one place (ARCHITECTURE.md#configuration) — rather than threaded
	// through every app's config.
	if sha := os.Getenv("GIT_SHA"); sha != "" {
		l = l.With(slog.String("git_sha", sha))
	}
	return &Logger{inner: l}
}

// NewSlog returns a raw *slog.Logger backed by zap for stdlib compatibility.
func NewSlog(cfg Config) *slog.Logger {
	return newSlog(cfg)
}

// Debug logs a message at debug level with the given key/value attribute pairs.
func (l *Logger) Debug(
	msg string,
	keysAndValues ...any,
) {
	l.log(slog.LevelDebug, msg, keysAndValues)
}

// Info logs a message at info level with the given key/value attribute pairs.
func (l *Logger) Info(
	msg string,
	keysAndValues ...any,
) {
	l.log(slog.LevelInfo, msg, keysAndValues)
}

// Warn logs a message at warn level with the given key/value attribute pairs.
func (l *Logger) Warn(
	msg string,
	keysAndValues ...any,
) {
	l.log(slog.LevelWarn, msg, keysAndValues)
}

// Error logs a message at error level with the given key/value attribute pairs.
func (l *Logger) Error(
	msg string,
	keysAndValues ...any,
) {
	l.log(slog.LevelError, msg, keysAndValues)
}

// log records the caller's own program counter (skipping these wrapper frames) so
// the "caller" field points at the code that logged -- not this file. slog's own
// output methods would record THIS wrapper as the caller, which is why every line
// otherwise showed zap/logger.go:NN.
func (l *Logger) log(level slog.Level, msg string, keysAndValues []any) {
	ctx := context.Background()
	if !l.inner.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	// Skip [runtime.Callers, log, the exported Debug/Info/Warn/Error] -> real caller.
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(keysAndValues...)
	_ = l.inner.Handler().Handle(ctx, r)
}

// With returns a child logger that includes the given key/value pairs on every
// subsequent record.
func (l *Logger) With(keysAndValues ...any) interfaces.Logger {
	return &Logger{inner: l.inner.With(keysAndValues...)}
}

// WithContext returns a logger enriched with the correlation, trace, and span
// IDs found in ctx. The receiver is returned unchanged when none are present.
func (l *Logger) WithContext(ctx context.Context) interfaces.Logger {
	// Extract all context fields independently - do NOT early-return on any single empty
	// field
	correlationID := logctx.CorrelationID(ctx)
	traceID := logctx.TraceID(ctx)
	spanID := logctx.SpanID(ctx)

	// If all fields are empty, return unchanged logger
	if correlationID == "" && traceID == "" && spanID == "" {
		return l
	}

	// Build field list with non-empty values
	var fields []any
	if correlationID != "" {
		fields = append(fields, "correlation_id", correlationID)
	}
	if traceID != "" {
		fields = append(fields, "trace_id", traceID)
	}
	if spanID != "" {
		fields = append(fields, "span_id", spanID)
	}

	return &Logger{inner: l.inner.With(fields...)}
}

// newSlog creates the underlying *slog.Logger from config.
func newSlog(cfg Config) *slog.Logger {
	level := parseLevel(cfg.Level)

	var encoder zapcore.Encoder
	encoderCfg := zap.NewProductionEncoderConfig()
	// Canonical cross-service log schema: timestamp / level (lowercase) /
	// message / caller. Kept identical in the stdlib logger and the Python
	// structlog config so every service emits the same JSON shape.
	encoderCfg.TimeKey = "timestamp"
	encoderCfg.MessageKey = "message"
	encoderCfg.LevelKey = "level"
	encoderCfg.CallerKey = "caller"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.LowercaseLevelEncoder
	// The origin-aware stacktrace handler (below) owns the "stacktrace" field:
	// for an AppError it emits the stack where the error was created, not this
	// logging call site. Suppress zapcore's own Entry.Stack so it does not emit a
	// second, call-site "stacktrace" under the same key.
	encoderCfg.StacktraceKey = zapcore.OmitKey

	if cfg.Format == "text" {
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level)
	zapLogger := zap.New(core)

	var handler slog.Handler = zapslog.NewHandler(zapLogger.Core(), zapslog.WithCaller(true))

	// Attach a full stack trace to every Error-level (and above) record. The
	// zapslog bridge bypasses zap.AddStacktrace, so do it at the slog layer.
	handler = logctx.NewStacktraceHandler(handler, slog.LevelError)

	if cfg.RedactPII {
		return slog.New(&redactingHandler{inner: handler})
	}

	return slog.New(handler)
}

// parseLevel maps a textual level name to its zapcore.Level, defaulting to
// InfoLevel for unrecognized input.
func parseLevel(s string) zapcore.Level {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// redactingHandler wraps an slog.Handler and redacts PII from string values.
type redactingHandler struct {
	// inner is the delegate handler that receives records after PII redaction.
	inner slog.Handler
}

// Enabled reports whether the handler processes records at the given level by
// delegating to the wrapped handler.
func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts PII from the record's message and string attributes, adds the
// context correlation ID for raw slog users, then forwards to the wrapped
// handler.
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	r2 := slog.NewRecord(r.Time, r.Level, logctx.RedactPII(r.Message), r.PC)

	r.Attrs(func(a slog.Attr) bool {
		r2.AddAttrs(redactAttr(a))
		return true
	})

	// Add correlation_id from context for raw slog.Logger users (via NewSlog +
	// slog.InfoContext). The Logger wrapper handles all context fields
	// (correlation_id, trace_id, span_id) through WithContext() instead.
	if id := logctx.CorrelationID(ctx); id != "" {
		r2.AddAttrs(slog.String("correlation_id", id))
	}

	return h.inner.Handle(ctx, r2)
}

// WithAttrs returns a new handler whose wrapped handler has the given
// attributes added, with each attribute's string value PII-redacted first.
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(redacted)}
}

// WithGroup returns a new handler whose wrapped handler opens the named group,
// preserving PII redaction for subsequent records.
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// noRedactKey reports whether key names a structured field that must never be
// PII-scrubbed: its value is an opaque identifier (hex trace/span IDs, UUIDs,
// source locations) that can spuriously match PII patterns and whose redaction
// would break log<->trace correlation.
func noRedactKey(key string) bool {
	switch key {
	case "trace_id", "span_id", "correlation_id", "request_id", "caller", "stacktrace":
		return true
	default:
		return false
	}
}

// redactAttr returns a, with its string value PII-scrubbed, unless noRedactKey
// matches its key or its value is not a string, in which case a is returned
// unchanged.
func redactAttr(a slog.Attr) slog.Attr {
	if noRedactKey(a.Key) {
		return a
	}
	if a.Value.Kind() == slog.KindString {
		return slog.Attr{
			Key:   a.Key,
			Value: slog.StringValue(logctx.RedactPII(a.Value.String())),
		}
	}
	return a
}
