package interfaces

import "context"

// Logger provides structured, leveled logging.
//
// Implementations: zap-backed slog (default), stdlib slog, or any structured
// logger that supports key-value pairs.
//
// All foundation and service code depends on this interface, never on a
// concrete logger. Swap implementations via the logger.New factory.
type Logger interface {
	// Debug logs at DEBUG level with optional key-value pairs.
	Debug(msg string, keysAndValues ...any)

	// Info logs at INFO level with optional key-value pairs.
	Info(msg string, keysAndValues ...any)

	// Warn logs at WARN level with optional key-value pairs.
	Warn(msg string, keysAndValues ...any)

	// Error logs at ERROR level with optional key-value pairs.
	Error(msg string, keysAndValues ...any)

	// With returns a new Logger with the given key-value pairs pre-bound.
	With(keysAndValues ...any) Logger

	// WithContext returns a new Logger that extracts values (e.g. correlation ID)
	// from the context on each log call.
	WithContext(ctx context.Context) Logger
}
