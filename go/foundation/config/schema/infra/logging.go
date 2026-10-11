package infra

import (
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger"
)

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	// Kind selects the logger implementation ("zap" or "stdlib").
	Kind string `mapstructure:"kind"`

	// Level is the minimum log severity to emit (e.g. "debug", "info", "warn", "error").
	Level string `mapstructure:"level"`

	// Format controls the output encoding ("json" or "text").
	Format string `mapstructure:"format"`

	// RedactPII enables automatic redaction of personally identifiable information in log
	// output.
	RedactPII bool `mapstructure:"redact_pii"`
}

// The logger kinds LoggingConfig.Kind accepts (case-insensitive).
const (
	// loggerKindZap selects the zap logger, the default.
	loggerKindZap = "zap"
	// loggerKindStdlib selects the log/slog-backed logger.
	loggerKindStdlib = "stdlib"
)

// DefaultLoggingConfig returns a LoggingConfig with defaults.
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Kind:      loggerKindZap,
		Level:     "info",
		Format:    "json",
		RedactPII: true,
	}
}

// Validate returns an error if the configuration is invalid.
func (c LoggingConfig) Validate() error {
	kind := strings.ToLower(c.Kind)
	if kind != loggerKindZap && kind != loggerKindStdlib {
		return apperr.New(
			apperr.CodeInvalidInput,
			"unknown logger kind: "+c.Kind,
		)
	}
	return nil
}

// GetKind converts the config kind string to a logger.Kind.
func (c *LoggingConfig) GetKind() (logger.Kind, error) {
	switch strings.ToLower(c.Kind) {
	case loggerKindZap:
		return logger.KindZap, nil
	case loggerKindStdlib:
		return logger.KindStdlib, nil
	default:
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			"unknown logger kind: "+c.Kind,
		)
	}
}
