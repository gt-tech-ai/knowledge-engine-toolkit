package errors

import (
	stderrors "errors"

	"go.uber.org/multierr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// MarshalLogObject implements zapcore.ObjectMarshaler for AppError.
// Encodes code, message, the wrapped cause, the origin stack, and details
// (nested object). The cause and stack are what make a wrapped error
// diagnosable: without them, a generic "failed to query database" hides the
// real underlying failure (e.g. a Postgres "column does not exist").
func (e *AppError) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("code", string(e.Code))
	enc.AddString("message", e.Message)
	if e.Err != nil {
		enc.AddString("cause", e.Err.Error())
	}
	if stack := e.StackTrace(); stack != "" {
		enc.AddString("stack", stack)
	}
	if len(e.Details) > 0 {
		if err := enc.AddObject("details", zapMap(e.Details)); err != nil {
			return Wrap(err, CodeInternal, "encode error details")
		}
	}
	return nil
}

// ZapError returns a zap.Field for structured error logging. If err is an
// *AppError, it returns a namespace containing the error code, message, and
// stack. Otherwise it returns a plain zap.Error field.
func ZapError(err error) zap.Field {
	var appErr *AppError
	if stderrors.As(err, &appErr) {
		return zap.Object("error", appErr)
	}
	return zap.Error(err)
}

// ZapFields extracts all structured zap fields from an AppError. Returns:
// error_code, error_message, error_cause (if wrapped), error_stack (if
// captured), and error_details (if non-empty).
func ZapFields(err *AppError) []zap.Field {
	fields := []zap.Field{
		zap.String("error_code", string(err.Code)),
		zap.String("error_message", err.Message),
	}
	if err.Err != nil {
		fields = append(fields, zap.String("error_cause", err.Err.Error()))
	}
	if stack := err.StackTrace(); stack != "" {
		fields = append(fields, zap.String("error_stack", stack))
	}
	if len(err.Details) > 0 {
		details := make([]zap.Field, 0, len(err.Details))
		for k, v := range err.Details {
			details = append(details, zap.String(k, v))
		}
		fields = append(fields, zap.Object("error_details", zapFields(details)))
	}
	return fields
}

// LogError logs a single error with structured AppError fields. Transient
// errors (timeout, unavailable) are logged at Warn level. All other errors are
// logged at Error level.
func LogError(logger *zap.Logger, msg string, err error) {
	var appErr *AppError
	if stderrors.As(err, &appErr) {
		fields := ZapFields(appErr)
		if IsTransient(appErr) {
			logger.Warn(msg, fields...)
		} else {
			logger.Error(msg, fields...)
		}
		return
	}
	logger.Error(msg, zap.Error(err))
}

// LogErrors logs a combined multi-error, emitting one log entry per individual
// error. Each error is logged via LogError with its own structured fields.
func LogErrors(logger *zap.Logger, msg string, combined error) {
	for _, err := range Errors(combined) {
		LogError(logger, msg, err)
	}
}

// Combine merges multiple errors into a single error. nil errors are discarded.
// If all errors are nil, it returns nil. It delegates to multierr.Combine.
func Combine(errs ...error) error {
	return multierr.Combine(errs...)
}

// Append appends right into left, returning the combined error. Either value
// may be nil, in which case the other is returned unchanged. It delegates to
// multierr.Append.
func Append(left, right error) error {
	return multierr.Append(left, right)
}

// Errors extracts individual errors from a combined error. If the error is not
// a combined error, returns a single-element slice.
func Errors(err error) []error {
	return multierr.Errors(err)
}

// zapMap implements zapcore.ObjectMarshaler for map[string]string.
type zapMap map[string]string

// MarshalLogObject encodes each map entry as a string field on enc.
func (m zapMap) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	for k, v := range m {
		enc.AddString(k, v)
	}
	return nil
}

// zapFields implements zapcore.ObjectMarshaler for a slice of zap.Field.
type zapFields []zap.Field

// MarshalLogObject writes each field in the slice onto enc.
func (f zapFields) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	for _, field := range f {
		field.AddTo(enc)
	}
	return nil
}
