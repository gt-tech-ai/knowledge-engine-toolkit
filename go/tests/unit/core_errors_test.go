// Package core_test verifies the go/core/errors package: AppError construction,
// error wrapping, code classification, HTTP status mapping, Zap integration, and
// structured logging. No external dependencies are used -- all tests run in-process.
package unit_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestNewError tests that New constructs an AppError with the correct code and
// message, and that Error() returns the canonical "CODE: message" format.
//
// Why this test is important:
//   - Callers depend on the "CODE: message" format for log parsing and error
//     display; a deviation would break log-scraping alerts that key on error codes
//
// What it tests:
//   - Code, Message fields match the given values
//   - Error() returns "NOT_FOUND: user not found"
func TestNewError(t *testing.T) {
	t.Parallel()

	err := apperr.New(apperr.ErrNotFound, "user not found")

	assert.Equal(t, apperr.ErrNotFound, err.Code)
	assert.Equal(t, "user not found", err.Message)
	assert.Equal(t, "NOT_FOUND: user not found", err.Error())
}

// TestWrapError tests that Wrap produces an AppError with the given code and
// chains the original cause so errors.Is traversal works.
//
// Why this test is important:
//   - Retry logic and error categorisation use Is-based matching through wrap
//     chains; a broken chain silently misclassifies errors and breaks retries
//
// What it tests:
//   - Returned error is *AppError with the given code
//   - errors.Is(wrapped, cause) returns true through the chain
func TestWrapError(t *testing.T) {
	t.Parallel()

	cause := apperr.Sentinel("connection refused")
	err := apperr.Wrap(cause, apperr.ErrUnavailable, "database connection failed")

	appErr := &apperr.AppError{}
	ok := errors.As(err, &appErr)
	require.True(t, ok, "expected error to be of type *AppError")
	assert.Equal(t, apperr.ErrUnavailable, appErr.Code)
	assert.ErrorIs(t, err, cause, "wrapped error should chain to cause")
}

// TestWrapNilReturnsNil tests that Wrap(nil, ...) returns nil.
//
// Why this test is important:
//   - Callers forward errors from optional operations; wrapping a nil into a
//     non-nil AppError would turn "no error" into a spurious failure
//
// What it tests:
//   - Wrap(nil, ErrInternal, "msg") returns nil
func TestWrapNilReturnsNil(t *testing.T) {
	t.Parallel()

	result := apperr.Wrap(nil, apperr.ErrInternal, "should not wrap")
	assert.NoError(t, result, "Wrap(nil, ...) must return nil")
}

// TestIsErrorCode tests that Is correctly matches an error against its own code
// and does not falsely match unrelated codes.
//
// Why this test is important:
//   - Is is the primary branching mechanism in handlers and middleware; a false
//     positive match maps the wrong HTTP status and routes to the wrong recovery path
//
// What it tests:
//   - Is(notFoundErr, ErrNotFound) returns true
//   - Is(notFoundErr, ErrInternal) returns false
func TestIsErrorCode(t *testing.T) {
	t.Parallel()

	err := apperr.NotFound("document not found")

	assert.True(
		t,
		apperr.Is(err, apperr.ErrNotFound),
		"Is(err, ErrNotFound) should be true",
	)
	assert.False(
		t,
		apperr.Is(err, apperr.ErrInternal),
		"Is(err, ErrInternal) should be false",
	)
}

// TestIsWrappedError tests that Is unwraps through a non-AppError wrapper (a Join)
// to match the underlying AppError code.
//
// Why this test is important:
//   - Middleware wraps errors before returning them; Is must see through multiple
//     layers or every wrapped error would be misclassified
//
// What it tests:
//   - Is(Join(notFoundErr), ErrNotFound) returns true
func TestIsWrappedError(t *testing.T) {
	t.Parallel()

	inner := apperr.NotFound("inner error")
	wrapped := apperr.Join(inner)

	assert.True(
		t,
		apperr.Is(wrapped, apperr.ErrNotFound),
		"Is should match wrapped error codes",
	)
}

// TestConvenienceConstructors tests that each convenience constructor sets the
// correct error code.
//
// Why this test is important:
//   - The constructors are the primary API; callers must not import raw code
//     constants, so a wrong code on a constructor silently misroutes every caller
//
// What it tests:
//   - NotFound, InvalidInput, Unauthorized, Forbidden, Conflict, Internal,
//     Timeout, Unavailable, QualityFailed, Upstream each produce the matching code
func TestConvenienceConstructors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fn   func(string) *apperr.AppError
		code apperr.ErrorCode
	}{
		{apperr.NotFound, apperr.ErrNotFound},
		{apperr.InvalidInput, apperr.ErrInvalidInput},
		{apperr.Unauthorized, apperr.ErrUnauthorized},
		{apperr.Forbidden, apperr.ErrForbidden},
		{apperr.Conflict, apperr.ErrConflict},
		{apperr.Internal, apperr.ErrInternal},
		{apperr.Timeout, apperr.ErrTimeout},
		{apperr.Unavailable, apperr.ErrUnavailable},
		{apperr.QualityFailed, apperr.ErrQualityFailed},
		{apperr.Upstream, apperr.CodeUpstream},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			t.Parallel()
			err := tt.fn("test message")
			assert.Equal(t, tt.code, err.Code)
		})
	}
}

// TestWithDetails tests that WithDetails attaches structured context to an
// error without modifying the original.
//
// Why this test is important:
//   - Safe detail accumulation across call layers requires immutability; mutating
//     the original error would corrupt the context seen by outer layers
//
// What it tests:
//   - Returned error contains the new detail key
//   - Original error's Details map is not mutated
func TestWithDetails(t *testing.T) {
	t.Parallel()

	err := apperr.NotFound("user not found")
	detailed := apperr.WithDetails(err, map[string]string{"user_id": "123"})

	assert.Equal(t, "123", detailed.Details["user_id"], "details should contain user_id")
	// Confirm the original is not mutated.
	if err.Details != nil {
		assert.NotEqual(
			t,
			"123",
			err.Details["user_id"],
			"original error must not be modified",
		)
	}
}

// TestIsTransient tests that Timeout and Unavailable errors are classified as
// transient.
//
// Why this test is important:
//   - Retry logic uses IsTransient to decide which errors are worth retrying;
//     a wrong classification causes either infinite retries on permanent errors
//     or no retries on recoverable ones
//
// What it tests:
//   - Timeout and Unavailable return IsTransient=true
//   - NotFound returns IsTransient=false
func TestIsTransient(t *testing.T) {
	t.Parallel()

	assert.True(
		t,
		apperr.IsTransient(apperr.Timeout("timeout")),
		"Timeout should be transient",
	)
	assert.True(
		t,
		apperr.IsTransient(apperr.Unavailable("unavail")),
		"Unavailable should be transient",
	)
	assert.False(
		t,
		apperr.IsTransient(apperr.NotFound("nf")),
		"NotFound should not be transient",
	)
}

// TestIsPermanent tests that NotFound and InvalidInput errors are classified as
// permanent.
//
// Why this test is important:
//   - Retry logic must not waste attempts on non-recoverable errors; a wrong
//     IsPermanent classification causes costly retries that will always fail
//
// What it tests:
//   - NotFound and InvalidInput return IsPermanent=true
//   - Timeout returns IsPermanent=false
func TestIsPermanent(t *testing.T) {
	t.Parallel()

	assert.True(
		t,
		apperr.IsPermanent(apperr.NotFound("nf")),
		"NotFound should be permanent",
	)
	assert.True(
		t,
		apperr.IsPermanent(apperr.InvalidInput("bad")),
		"InvalidInput should be permanent",
	)
	assert.False(
		t,
		apperr.IsPermanent(apperr.Timeout("timeout")),
		"Timeout should not be permanent",
	)
}

// TestToHTTPStatus tests the canonical error-code to HTTP status mapping.
//
// Why this test is important:
//   - Handlers use this table to avoid hardcoded status codes; a wrong mapping
//     returns the wrong HTTP status to clients and breaks SDK error handling
//
// What it tests:
//   - ErrNotFound->404, ErrInvalidInput->400, ErrUnauthorized->401,
//     ErrForbidden->403, ErrConflict->409, ErrInternal->500,
//     ErrTimeout->504, ErrUnavailable->503
func TestToHTTPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code   apperr.ErrorCode
		status int
	}{
		{apperr.ErrNotFound, 404},
		{apperr.ErrInvalidInput, 400},
		{apperr.ErrUnauthorized, 401},
		{apperr.ErrForbidden, 403},
		{apperr.ErrConflict, 409},
		{apperr.ErrInternal, 500},
		{apperr.ErrTimeout, 504},
		{apperr.ErrUnavailable, 503},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.status, apperr.ToHTTPStatus(tt.code))
		})
	}
}

// ---------------------------------------------------------------------------
// Zap integration
// ---------------------------------------------------------------------------

// TestMarshalLogObject tests that AppError.MarshalLogObject writes code,
// message, and details fields to a Zap encoder.
//
// Why this test is important:
//   - Structured log pipelines parse these fields for alerting and dashboards;
//     a missing or misnamed field silently breaks log-based alerts
//
// What it tests:
//   - MarshalLogObject writes code, message, and details without error
func TestMarshalLogObject(t *testing.T) {
	t.Parallel()

	appErr := apperr.WithDetails(
		apperr.NotFound("record not found"),
		map[string]string{"id": "123"},
	)

	enc := zapcore.NewMapObjectEncoder()
	require.NoError(
		t,
		appErr.MarshalLogObject(enc),
		"MarshalLogObject must not return error",
	)

	assert.Equal(t, string(apperr.CodeNotFound), enc.Fields["code"])
	assert.Equal(t, "record not found", enc.Fields["message"])
	assert.Contains(t, enc.Fields, "details", "details field must be present")
}

// TestZapErrorAppError tests that ZapError returns an ObjectMarshalerType field
// for AppError values.
//
// Why this test is important:
//   - ObjectMarshalerType enables rich structured logging of domain errors with
//     nested code and message fields; a wrong type produces flat string logging
//     that cannot be parsed by log pipelines
//
// What it tests:
//   - ZapError(appErr) returns field Key="error" with Type=ObjectMarshalerType
func TestZapErrorAppError(t *testing.T) {
	t.Parallel()

	appErr := apperr.NotFound("record not found")
	field := apperr.ZapError(appErr)

	assert.Equal(t, "error", field.Key)
	assert.Equal(
		t,
		zapcore.ObjectMarshalerType,
		field.Type,
		"AppError should use ObjectMarshalerType",
	)
}

// TestZapErrorPlainError tests that ZapError falls back to ErrorType for plain
// Go errors.
//
// Why this test is important:
//   - Non-AppError values must still produce useful log entries; a panic or
//     empty field on a plain error would suppress the error from logs entirely
//
// What it tests:
//   - ZapError(plainErr) returns field Key="error" with Type=ErrorType
func TestZapErrorPlainError(t *testing.T) {
	t.Parallel()

	plainErr := apperr.Sentinel("something went wrong")
	field := apperr.ZapError(plainErr)

	assert.Equal(t, "error", field.Key)
	assert.Equal(t, zapcore.ErrorType, field.Type, "plain error should use ErrorType")
}

// TestZapFields tests that ZapFields returns error_code and error_message
// fields with the correct values.
//
// Why this test is important:
//   - Log processors index these exact field names for error-rate dashboards
//     and on-call alerts; a renamed field silently breaks the alerting pipeline
//
// What it tests:
//   - ZapFields returns error_code=CodeInternal and error_message matching the error
func TestZapFields(t *testing.T) {
	t.Parallel()

	appErr := apperr.Internal("database connection failed")
	fields := apperr.ZapFields(appErr)

	fieldMap := make(map[string]zap.Field, len(fields))
	for _, f := range fields {
		fieldMap[f.Key] = f
	}

	codeField, ok := fieldMap["error_code"]
	require.True(t, ok, "expected error_code field")
	assert.Equal(t, string(apperr.CodeInternal), codeField.String)

	msgField, ok := fieldMap["error_message"]
	require.True(t, ok, "expected error_message field")
	assert.Equal(t, "database connection failed", msgField.String)
}

// TestLogErrorTransient tests that LogError logs transient errors at Warn level.
//
// Why this test is important:
//   - On-call rules apply different severity thresholds based on log level;
//     a transient error logged at Error would page the on-call engineer for a
//     retryable failure that would resolve on its own
//
// What it tests:
//   - LogError with a Timeout error logs exactly one entry at WarnLevel
func TestCoreLogErrorTransient(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogError(logger, "request failed", apperr.Timeout("connection timed out"))

	entries := observed.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	assert.Equal(t, zap.WarnLevel, entries[0].Level, "transient error should log at Warn")
}

// TestLogErrorPermanent tests that LogError logs permanent errors at Error
// level.
//
// Why this test is important:
//   - Non-retryable failures must trigger high-severity alerts; logging them
//     at Warn would suppress paging and let permanent failures go unnoticed
//
// What it tests:
//   - LogError with a NotFound error logs exactly one entry at ErrorLevel
func TestCoreLogErrorPermanent(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogError(logger, "lookup failed", apperr.NotFound("record missing"))

	entries := observed.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	assert.Equal(
		t,
		zap.ErrorLevel,
		entries[0].Level,
		"permanent error should log at Error",
	)
}

// TestLogErrorPlainError tests that LogError treats plain Go errors as permanent
// failures and logs them at Error level.
//
// Why this test is important:
//   - Unexpected untyped errors must be treated as permanent; logging them at
//     Warn would hide crashes and data-loss events from high-severity alert rules
//
// What it tests:
//   - LogError with a plain errors.New error logs exactly one entry at ErrorLevel
func TestCoreLogErrorPlainError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogError(logger, "unexpected failure", apperr.Sentinel("disk full"))

	entries := observed.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	assert.Equal(t, zap.ErrorLevel, entries[0].Level, "plain error should log at Error")
}
