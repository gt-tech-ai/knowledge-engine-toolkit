// Package errors provides application error types and codes.
package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"strings"
)

// ErrorCode represents application error codes.
type ErrorCode string

// Error codes.
const (
	// CodeUnknown is the fallback code for errors that are not AppErrors or carry no code.
	CodeUnknown ErrorCode = "UNKNOWN"
	// CodeInternal marks an unexpected server-side failure.
	CodeInternal ErrorCode = "INTERNAL"
	// CodeNotFound marks a missing resource.
	CodeNotFound ErrorCode = "NOT_FOUND"
	// CodeUnauthorized marks a missing or invalid authentication credential.
	CodeUnauthorized ErrorCode = "UNAUTHORIZED"
	// CodeForbidden marks an authenticated caller lacking permission for the action.
	CodeForbidden ErrorCode = "FORBIDDEN"
	// CodeInvalidInput marks a malformed or otherwise rejected request payload.
	CodeInvalidInput ErrorCode = "INVALID_INPUT"
	// CodeConflict marks a state conflict (e.g. a duplicate or concurrent modification).
	CodeConflict ErrorCode = "CONFLICT"
	// CodeTimeout marks an operation that exceeded its deadline.
	CodeTimeout ErrorCode = "TIMEOUT"
	// CodeCanceled marks an operation that was canceled by the caller.
	CodeCanceled ErrorCode = "CANCELED"
	// CodeUnavailable marks a temporarily unreachable dependency.
	CodeUnavailable ErrorCode = "UNAVAILABLE"
	// CodeIngestion marks a failure in the document ingestion pipeline.
	CodeIngestion ErrorCode = "INGESTION_ERROR"
	// CodeQualityFailed marks a failed quality gate.
	CodeQualityFailed ErrorCode = "QUALITY_FAILED"
	// CodeUpstream marks a failure originating in an upstream dependency.
	CodeUpstream ErrorCode = "UPSTREAM"
	// CodeResourceExhausted marks a request rejected because a quota or budget is
	// spent. It is neither transient (retrying before the window resets only burns
	// the quota check) nor permanent (the request succeeds once the window resets).
	CodeResourceExhausted ErrorCode = "RESOURCE_EXHAUSTED"

	// Aliases for compatibility with classify.go

	// ErrUnknown aliases CodeUnknown.
	ErrUnknown = CodeUnknown
	// ErrInternal aliases CodeInternal.
	ErrInternal = CodeInternal
	// ErrNotFound aliases CodeNotFound.
	ErrNotFound = CodeNotFound
	// ErrUnauthorized aliases CodeUnauthorized.
	ErrUnauthorized = CodeUnauthorized
	// ErrForbidden aliases CodeForbidden.
	ErrForbidden = CodeForbidden
	// ErrInvalidInput aliases CodeInvalidInput.
	ErrInvalidInput = CodeInvalidInput
	// ErrConflict aliases CodeConflict.
	ErrConflict = CodeConflict
	// ErrTimeout aliases CodeTimeout.
	ErrTimeout = CodeTimeout
	// ErrUnavailable aliases CodeUnavailable.
	ErrUnavailable = CodeUnavailable
	// ErrIngestion aliases CodeIngestion.
	ErrIngestion = CodeIngestion
	// ErrQualityFailed aliases CodeQualityFailed.
	ErrQualityFailed = CodeQualityFailed
)

// AppError represents an application error with code and context.
type AppError struct {
	// Err is the underlying wrapped error, if any.
	Err error

	// Details holds optional key-value context about the error.
	Details map[string]string

	// Code is the application-level error code.
	Code ErrorCode

	// Message is a human-readable description of the error.
	Message string

	// stack holds the program counters captured at the point the error
	// originated (New, or Wrap of a non-AppError). It is rendered lazily by
	// StackTrace so the resolution cost is only paid when the error is logged.
	stack []uintptr
}

// Error implements the error interface.
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Message, e.Err.Error())
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped error.
func (e *AppError) Unwrap() error {
	return e.Err
}

// StackTrace renders the captured origin stack as a newline-separated
// "func\n\tfile:line" trace, skipping the leading frames that belong to this
// errors package so the trace starts at the application code that created the
// error. Returns an empty string when no stack was captured.
func (e *AppError) StackTrace() string {
	if len(e.stack) == 0 {
		return ""
	}
	var sb strings.Builder
	frames := runtime.CallersFrames(e.stack)
	skipping := true
	prefix := pkgPrefix()
	for {
		frame, more := frames.Next()
		// Drop the New/Wrap/helper frames at the head of the trace.
		if skipping && strings.HasPrefix(frame.Function, prefix) {
			if !more {
				break
			}
			continue
		}
		skipping = false
		fmt.Fprintf(&sb, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// pkgPrefix returns this package's import path plus the trailing dot that starts
// every function name in it (e.g. "example.com/mod/go/core/errors."). It is read
// from the AppError type rather than written down, so StackTrace recognizes this
// package's frames whatever module path or checkout it is built from.
func pkgPrefix() string {
	return reflect.TypeFor[AppError]().PkgPath() + "."
}

// callers captures the program counters of the calling goroutine's stack,
// excluding runtime.Callers and callers itself.
func callers() []uintptr {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	return pcs[:n]
}

// stackFromOrCapture preserves the origin stack when wrapping an existing
// AppError; otherwise it captures a fresh stack at the wrap site.
func stackFromOrCapture(err error) []uintptr {
	var appErr *AppError
	if stderrors.As(err, &appErr) && len(appErr.stack) > 0 {
		return appErr.stack
	}
	return callers()
}

// New creates a new AppError.
func New(code ErrorCode, message string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		stack:   callers(),
	}
}

// Wrap wraps an existing error with an AppError.
// Returns nil if err is nil.
func Wrap(err error, code ErrorCode, message string) error {
	if err == nil {
		return nil
	}
	return &AppError{
		Code:    code,
		Message: message,
		Err:     err,
		stack:   stackFromOrCapture(err),
	}
}

// Is checks if an error (or any error in its chain) has the given ErrorCode.
func Is(err error, code ErrorCode) bool {
	if appErr, ok := stderrors.AsType[*AppError](err); ok {
		return appErr.Code == code
	}
	return false
}

// Code extracts the ErrorCode from an error.
// Returns CodeUnknown if the error is not an AppError.
func Code(err error) ErrorCode {
	if appErr, ok := stderrors.AsType[*AppError](err); ok {
		return appErr.Code
	}
	return CodeUnknown
}

// CodeOr extracts err's ErrorCode like Code, but returns fallback when err
// carries no code (an uncoded third-party error, or nil). Use it to wrap a
// client error without discarding a code the client already chose.
func CodeOr(err error, fallback ErrorCode) ErrorCode {
	if code := Code(err); code != CodeUnknown {
		return code
	}
	return fallback
}

// ContextCode extracts err's ErrorCode like CodeOr, but first maps an uncoded
// context error in the chain: context.DeadlineExceeded to CodeTimeout and
// context.Canceled to CodeCanceled. Use it to wrap the error of a wait that a
// context ended (ctx.Err(), a library's deadline error) with the code that keeps
// retry classification and the transport status right.
func ContextCode(err error, fallback ErrorCode) ErrorCode {
	if code := Code(err); code != CodeUnknown {
		return code
	}
	switch {
	case stderrors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	case stderrors.Is(err, context.Canceled):
		return CodeCanceled
	default:
		return fallback
	}
}

// WithDetails returns a copy of the error with additional details.
func WithDetails(err *AppError, details map[string]string) *AppError {
	copied := *err
	copied.Details = make(map[string]string, len(details))
	maps.Copy(copied.Details, details)
	return &copied
}

// Helper functions for creating common errors.

// Internal creates an internal error.
func Internal(message string) *AppError {
	return New(CodeInternal, message)
}

// NotFound creates a not found error.
func NotFound(message string) *AppError {
	return New(CodeNotFound, message)
}

// Unauthorized creates an unauthorized error.
func Unauthorized(message string) *AppError {
	return New(CodeUnauthorized, message)
}

// Forbidden creates a forbidden error.
func Forbidden(message string) *AppError {
	return New(CodeForbidden, message)
}

// InvalidInput creates an invalid input error.
func InvalidInput(message string) *AppError {
	return New(CodeInvalidInput, message)
}

// Conflict creates a conflict error.
func Conflict(message string) *AppError {
	return New(CodeConflict, message)
}

// Timeout creates a timeout error.
func Timeout(message string) *AppError {
	return New(CodeTimeout, message)
}

// Unavailable creates an unavailable error.
func Unavailable(message string) *AppError {
	return New(CodeUnavailable, message)
}

// Ingestion creates an ingestion error.
func Ingestion(message string) *AppError {
	return New(CodeIngestion, message)
}

// QualityFailed creates a quality gate failure error.
func QualityFailed(message string) *AppError {
	return New(CodeQualityFailed, message)
}

// Upstream creates an upstream dependency error.
func Upstream(message string) *AppError {
	return New(CodeUpstream, message)
}
