package decorate

import (
	"context"
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// loggingMW logs an operation at Debug on entry and Debug on failure (suppressed in
// staging/prod where level=info). subjectField/msgPrefix are the tier-specific
// labels: repo ("repo"/"repository") vs service ("service"/"service"). Logs through
// the context logger so trace ids correlate; the outermost recovery/transport seam
// owns Error-level logging.
type loggingMW struct {
	// logger emits the entry/failure logs through the context logger.
	logger interfaces.Logger
	// name identifies the repo/service instance; the subjectField value.
	name string
	// subjectField is the tier-specific field key (e.g. "repo" or "service").
	subjectField string
	// msgPrefix is the tier-specific log-message prefix (e.g. "repository" or "service").
	msgPrefix string
}

// NewLogging returns a logging middleware for name, tagging entries with subjectField
// (=name) and messages "<msgPrefix>.<op>".
func NewLogging(
	logger interfaces.Logger,
	name, subjectField, msgPrefix string,
) OpMiddleware {
	return loggingMW{
		logger:       logger,
		name:         name,
		subjectField: subjectField,
		msgPrefix:    msgPrefix,
	}
}

// WrapOp logs the operation on entry and, on failure, at Debug with the error.
func (m loggingMW) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	l := m.logger.WithContext(ctx)
	l.Debug(m.msgPrefix+"."+op, m.subjectField, m.name, "custom_operation", op)
	err := next(ctx)
	if err != nil {
		l.Debug(
			fmt.Sprintf("%s.%s failed", m.msgPrefix, op),
			m.subjectField, m.name, "custom_operation", op, "error", err,
		)
	}
	return err
}
