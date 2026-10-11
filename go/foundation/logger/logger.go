// Package logger provides structured logging with multiple backend implementations.
//
// Use New() or NewFromConfig() to create a logger instance. The factory pattern allows
// selecting between zap-backed or stdlib slog implementations at runtime.
//
// Example:
//
//	l, err := logger.New(logger.KindZap, logger.WithLevel("debug"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	l.Info("application started")
//
// Shared utilities (WithCorrelationID, CorrelationID, RedactPII) are available from this
// package.
package logger

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/stdlib"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/zap"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which logger implementation to use.
type Kind int

const (
	// KindZap uses a zap-backed logger with PII redaction.
	// Suitable for production with structured JSON logging.
	KindZap Kind = iota

	// KindStdlib uses pure stdlib slog logger.
	// Suitable for environments where zero external dependencies are required.
	KindStdlib
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindZap:
		return "zap"
	case KindStdlib:
		return "stdlib"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates a Logger of the specified kind with optional functional options.
// Returns an error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.Logger, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a Logger from a Config struct.
// Returns an error if the kind is unknown.
func NewFromConfig(cfg Config) (interfaces.Logger, error) {
	switch cfg.Kind {
	case KindZap:
		zapCfg := zap.Config{
			Level:     cfg.Level,
			Format:    cfg.Format,
			RedactPII: cfg.RedactPII,
		}
		return zap.New(zapCfg), nil

	case KindStdlib:
		stdlibCfg := stdlib.Config{
			Level:     cfg.Level,
			AddSource: cfg.AddSource,
		}
		return stdlib.New(stdlibCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown logger kind: %v", cfg.Kind),
		)
	}
}

// WithCorrelationID returns a context with the given correlation ID. Use this
// to propagate request tracing IDs through the call stack.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return logctx.WithCorrelationID(ctx, id)
}

// CorrelationID extracts the correlation ID from context. Returns empty string
// if no correlation ID is present.
func CorrelationID(ctx context.Context) string {
	return logctx.CorrelationID(ctx)
}

// TraceID extracts the OpenTelemetry trace ID from context. Returns the hex-encoded
// trace ID string if a valid span context is present, empty string otherwise.
// This enables log-trace correlation in Grafana (Loki → Tempo linking).
func TraceID(ctx context.Context) string {
	return logctx.TraceID(ctx)
}

// SpanID extracts the OpenTelemetry span ID from context. Returns the hex-encoded
// span ID string if a valid span context is present, empty string otherwise.
// This enables drill-down from log entries to specific spans within a trace.
func SpanID(ctx context.Context) string {
	return logctx.SpanID(ctx)
}

// RedactPII replaces PII patterns (email, phone, SSN, IPv4, auth tokens) with
// "[REDACTED]". Use this to sanitize log messages before recording them.
func RedactPII(s string) string {
	return logctx.RedactPII(s)
}
