package logctx

import (
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// The log schemas a logger can emit (ARCHITECTURE.md#logging).
const (
	// SchemaV1 is the original output: timestamp, level, message, caller, and an
	// error logged as the backend renders it. It is the default.
	SchemaV1 = "v1"

	// SchemaV2 is the unified field set: v1's keys with a millisecond UTC
	// timestamp and a "dir/file.go:line" caller, plus service and env on every
	// line and a logged error flattened to its message beside its error_code.
	SchemaV2 = "v2"
)

// ErrorKey is the attribute key a caller logs an error under; under SchemaV2 it
// holds the error's message and ErrorCodeKey sits beside it.
const ErrorKey = "error"

// ErrorCodeKey is the attribute key carrying a logged error's code under SchemaV2.
const ErrorCodeKey = "error_code"

// ServiceKey is the attribute key naming the emitting service under SchemaV2.
const ServiceKey = "service"

// EnvKey is the attribute key naming the deploy environment under SchemaV2.
const EnvKey = "env"

// TimestampLayoutV2 renders a SchemaV2 timestamp: RFC 3339 with milliseconds,
// "Z" for the UTC times FormatTimeV2 passes it.
const TimestampLayoutV2 = "2006-01-02T15:04:05.000Z07:00"

// IsSchema reports whether name is a known schema; "" counts as SchemaV1.
func IsSchema(name string) bool {
	return name == "" || name == SchemaV1 || name == SchemaV2
}

// FormatTimeV2 renders t as a SchemaV2 timestamp.
func FormatTimeV2(t time.Time) string {
	return t.UTC().Format(TimestampLayoutV2)
}

// ShortCaller renders a source location as "dir/file.go:line", the form the zap
// backend's caller takes, so both backends emit the same caller string.
func ShortCaller(file string, line int) string {
	short := filepath.Join(filepath.Base(filepath.Dir(file)), filepath.Base(file))

	return short + ":" + strconv.Itoa(line)
}

// SchemaV2Attrs returns the service and env attributes bound to every SchemaV2
// line, as key/value pairs for slog.Logger.With; an empty value is left out.
func SchemaV2Attrs(service, env string) []any {
	var attrs []any

	if service != "" {
		attrs = append(attrs, ServiceKey, service)
	}

	if env != "" {
		attrs = append(attrs, EnvKey, env)
	}

	return attrs
}

// flattenError returns attr unchanged unless it is an error under ErrorKey, which
// becomes its message string followed by its ErrorCodeKey attribute (CodeUnknown
// for an uncoded error).
func flattenError(attr slog.Attr) []slog.Attr {
	if attr.Key != ErrorKey {
		return []slog.Attr{attr}
	}

	err, ok := attr.Value.Resolve().Any().(error)
	if !ok || err == nil {
		return []slog.Attr{attr}
	}

	return []slog.Attr{
		slog.String(ErrorKey, err.Error()),
		slog.String(ErrorCodeKey, string(apperr.Code(err))),
	}
}

// flattenErrors returns attrs with every logged error flattened by flattenError.
func flattenErrors(attrs []slog.Attr) []slog.Attr {
	flat := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		flat = append(flat, flattenError(attr)...)
	}

	return flat
}
