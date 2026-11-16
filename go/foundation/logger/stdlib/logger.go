// Package stdlib provides a pure stdlib slog logger implementation.
package stdlib

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
)

// logCallerSkip is the frames between runtime.Callers and the real log caller.
const logCallerSkip = 3

// Compile-time interface assertion.
var _ interfaces.Logger = (*Logger)(nil)

// Config holds stdlib-specific logger configuration.
type Config struct {
	// Level sets the minimum log severity (debug, info, warn, error).
	Level string

	// Schema selects the field set: logctx.SchemaV1 (default, "" too) or
	// logctx.SchemaV2 (ARCHITECTURE.md#logging).
	Schema string

	// Service names the emitting service, bound to every SchemaV2 line.
	Service string

	// Env names the deploy environment, bound to every SchemaV2 line.
	Env string

	// AddSource enables source code location in log records; SchemaV2 forces it on.
	AddSource bool
}

// DefaultConfig returns stdlib-specific configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Level:     "info",
		Schema:    logctx.SchemaV1,
		Service:   "",
		Env:       "",
		AddSource: true,
	}
}

// Logger implements interfaces.Logger using pure stdlib slog.
type Logger struct {
	// inner is the slog.Logger that performs the actual structured logging.
	inner *slog.Logger
}

// New creates a stdlib slog-backed logger from the given config.
func New(cfg *Config) *Logger {
	return &Logger{inner: slogBridge(cfg)}
}

// NewSlog returns a raw *slog.Logger for stdlib compatibility.
func NewSlog(cfg *Config) *slog.Logger {
	return slogBridge(cfg)
}

// Debug logs a message at debug level with the given key/value attribute pairs.
func (l *Logger) Debug(msg string, keysAndValues ...any) {
	l.log(slog.LevelDebug, msg, keysAndValues)
}

// Info logs a message at info level with the given key/value attribute pairs.
func (l *Logger) Info(msg string, keysAndValues ...any) {
	l.log(slog.LevelInfo, msg, keysAndValues)
}

// Warn logs a message at warn level with the given key/value attribute pairs.
func (l *Logger) Warn(msg string, keysAndValues ...any) {
	l.log(slog.LevelWarn, msg, keysAndValues)
}

// Error logs a message at error level with the given key/value attribute pairs.
func (l *Logger) Error(msg string, keysAndValues ...any) {
	l.log(slog.LevelError, msg, keysAndValues)
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

// log builds the record with the real caller's PC, so the caller field names the
// line that called Debug/Info/Warn/Error rather than this wrapper.
func (l *Logger) log(level slog.Level, msg string, keysAndValues []any) {
	ctx := context.Background()
	if !l.inner.Enabled(ctx, level) {
		return
	}

	var pcs [1]uintptr

	// Skip [runtime.Callers, log, the exported Debug/Info/Warn/Error] -> real caller.
	runtime.Callers(logCallerSkip, pcs[:])

	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(keysAndValues...)

	_ = l.inner.Handler().Handle(ctx, r)
}

// slogBridge creates the underlying *slog.Logger from config.
func slogBridge(cfg *Config) *slog.Logger {
	if cfg.Schema == logctx.SchemaV2 {
		opts := &slog.HandlerOptions{
			Level:       parseLevel(cfg.Level),
			AddSource:   true,
			ReplaceAttr: canonicalAttrV2,
		}
		handler := logctx.NewSchemaV2StacktraceHandler(
			slog.NewJSONHandler(os.Stdout, opts), slog.LevelError)

		return slog.New(handler).With(logctx.SchemaV2Attrs(cfg.Service, cfg.Env)...)
	}

	opts := &slog.HandlerOptions{
		Level:       parseLevel(cfg.Level),
		AddSource:   cfg.AddSource,
		ReplaceAttr: canonicalAttr,
	}

	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)

	// Attach a full stack trace to every Error-level (and above) record.
	handler = logctx.NewStacktraceHandler(handler, slog.LevelError)

	return slog.New(handler)
}

// canonicalAttrV2 applies canonicalAttr, then the SchemaV2 shapes: a millisecond
// UTC timestamp and a "dir/file.go:line" caller string, the zap backend's form.
func canonicalAttrV2(groups []string, attr slog.Attr) slog.Attr {
	attr = canonicalAttr(groups, attr)
	if len(groups) != 0 {
		return attr
	}

	switch attr.Key {
	case "timestamp":
		if t, ok := attr.Value.Any().(time.Time); ok {
			attr.Value = slog.StringValue(logctx.FormatTimeV2(t))
		}
	case "caller":
		if src, ok := attr.Value.Any().(*slog.Source); ok {
			attr.Value = slog.StringValue(logctx.ShortCaller(src.File, src.Line))
		}
	default:
		// No other key changes under v2.
	}

	return attr
}

// canonicalAttr rewrites slog's built-in keys to the cross-service schema
// (timestamp / level[lowercase] / message / caller) so the stdlib logger emits
// the same JSON shape as the zap logger and the Python structlog config.
func canonicalAttr(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) != 0 {
		return attr
	}

	switch attr.Key {
	case slog.TimeKey:
		attr.Key = "timestamp"
	case slog.MessageKey:
		attr.Key = "message"
	case slog.SourceKey:
		attr.Key = "caller"
	case slog.LevelKey:
		if lvl, ok := attr.Value.Any().(slog.Level); ok {
			attr.Value = slog.StringValue(strings.ToLower(lvl.String()))
		}
	default:
		// No other value changes the outcome.
	}

	return attr
}

// parseLevel maps a textual level name to its slog.Level, defaulting to
// LevelInfo for unrecognized input.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		// Any other value takes the shared result below.
	}

	return slog.LevelInfo
}
