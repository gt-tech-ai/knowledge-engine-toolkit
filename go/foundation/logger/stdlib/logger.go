// Package stdlib provides a pure stdlib slog logger implementation.
package stdlib

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
)

// Compile-time interface assertion.
var _ interfaces.Logger = (*Logger)(nil)

// Config holds stdlib-specific logger configuration.
type Config struct {
	// Level sets the minimum log severity (debug, info, warn, error).
	Level string

	// AddSource enables source code location in log records.
	AddSource bool
}

// DefaultConfig returns stdlib-specific configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Level:     "info",
		AddSource: true,
	}
}

// Logger implements interfaces.Logger using pure stdlib slog.
type Logger struct {
	// inner is the slog.Logger that performs the actual structured logging.
	inner *slog.Logger
}

// New creates a stdlib slog-backed logger from the given config.
func New(cfg Config) *Logger {
	return &Logger{inner: newSlog(cfg)}
}

// NewSlog returns a raw *slog.Logger for stdlib compatibility.
func NewSlog(cfg Config) *slog.Logger {
	return newSlog(cfg)
}

// Debug logs a message at debug level with the given key/value attribute pairs.
func (l *Logger) Debug(msg string, keysAndValues ...any) {
	l.inner.Log(context.Background(), slog.LevelDebug, msg, keysAndValues...)
}

// Info logs a message at info level with the given key/value attribute pairs.
func (l *Logger) Info(msg string, keysAndValues ...any) {
	l.inner.Log(context.Background(), slog.LevelInfo, msg, keysAndValues...)
}

// Warn logs a message at warn level with the given key/value attribute pairs.
func (l *Logger) Warn(msg string, keysAndValues ...any) {
	l.inner.Log(context.Background(), slog.LevelWarn, msg, keysAndValues...)
}

// Error logs a message at error level with the given key/value attribute pairs.
func (l *Logger) Error(msg string, keysAndValues ...any) {
	l.inner.Log(context.Background(), slog.LevelError, msg, keysAndValues...)
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

	opts := &slog.HandlerOptions{
		Level:       level,
		AddSource:   cfg.AddSource,
		ReplaceAttr: canonicalAttr,
	}

	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	// Attach a full stack trace to every Error-level (and above) record.
	handler = logctx.NewStacktraceHandler(handler, slog.LevelError)
	return slog.New(handler)
}

// canonicalAttr rewrites slog's built-in keys to the cross-service schema
// (timestamp / level[lowercase] / message / caller) so the stdlib logger emits
// the same JSON shape as the zap logger and the Python structlog config.
func canonicalAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) != 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		a.Key = "timestamp"
	case slog.MessageKey:
		a.Key = "message"
	case slog.SourceKey:
		a.Key = "caller"
	case slog.LevelKey:
		if lvl, ok := a.Value.Any().(slog.Level); ok {
			a.Value = slog.StringValue(strings.ToLower(lvl.String()))
		}
	}
	return a
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
		return slog.LevelInfo
	}
}
