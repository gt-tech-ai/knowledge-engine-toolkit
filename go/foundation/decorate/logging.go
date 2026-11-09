package decorate

import (
	"context"
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// loggingMW logs an operation at Debug on entry and on failure (suppressed where the
// level is info), keyed by the tier: "<tier>.<op>" with "<tier>": name. Inner tiers log
// failures at Debug so the edge logs the error once: an edge tier's (Tier.Edge) failure
// is logged at Warn when its code is a client fault (an HTTP status below 500) and at
// Error otherwise.
type loggingMW struct {
	// logger receives the entries.
	logger interfaces.Logger

	// tier labels the entries ("pipeline", "service", …).
	tier string

	// name is the decorated subject (a stage, a service, a repository).
	name string

	// edge raises a failure from Debug to Warn or Error (see Tier.Edge).
	edge bool
}

// NewLogging returns the logging middleware for the subject name in tier.
func NewLogging(logger interfaces.Logger, tier, name string) OpMiddleware {
	return loggingMW{logger: logger, tier: tier, name: name}
}

// WrapOp logs op before running next and again if it fails.
func (m loggingMW) WrapOp(
	ctx context.Context,
	operation string,
	next func(context.Context) error,
) error {
	currentLogger := m.logger.WithContext(ctx)
	currentLogger.Debug(m.tier+"."+operation, m.tier, m.name, textOperation, operation)

	err := next(ctx)
	if err == nil {
		return nil
	}

	msg, fields := m.tier+"."+operation+" failed", []any{
		m.tier,
		m.name,
		textOperation,
		operation,
		"error",
		err,
	}
	switch {
	case !m.edge:
		currentLogger.Debug(msg, fields...)
	case apperr.ToHTTPStatus(apperr.Code(err)) < http.StatusInternalServerError:
		currentLogger.Warn(msg, fields...)
	default:
		currentLogger.Error(msg, fields...)
	}

	return err
}
