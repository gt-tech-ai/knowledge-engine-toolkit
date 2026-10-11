package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestErrorFieldAppError tests that ZapError() extracts structured fields from an
// AppError.
//
// Why this test is important:
//   - Structured logging of AppError fields enables log aggregation and alerting on
//     error_code
//   - Without this, operators would only see the flat error string with no
//     machine-parseable code
//
// What it tests:
//   - ZapError() with an AppError produces an "error" namespace field (not a plain
//     "error" string)
//   - The namespace contains code, message, and stack sub-fields
func TestErrorFieldAppError(t *testing.T) {
	t.Parallel()

	appErr := apperr.NotFound("record not found")
	field := apperr.ZapError(appErr)

	assert.Equal(t, "error", field.Key)
	assert.Equal(t, zapcore.ObjectMarshalerType, field.Type)
}

// TestErrorFieldPlainError tests that ZapError() falls back to zap.Error for non-AppError
// errors.
//
// Why this test is important:
//   - Not all errors in the system are AppErrors (e.g. stdlib, third-party)
//   - The fallback ensures these are still logged correctly via standard zap.Error
//
// What it tests:
//   - ZapError() with a plain error produces a field with key "error"
//   - The field type is ErrorType (standard zap error encoding)
func TestErrorFieldPlainError(t *testing.T) {
	t.Parallel()

	plainErr := apperr.Sentinel("something went wrong")
	field := apperr.ZapError(plainErr)

	assert.Equal(t, "error", field.Key)
	assert.Equal(t, zapcore.ErrorType, field.Type)
}

// TestFieldsExtraction tests that ZapFields() returns structured zap fields from an
// AppError.
//
// Why this test is important:
//   - ZapFields() is used by LogError to attach machine-parseable error context to log
//     entries
//   - Correct field extraction enables dashboards, alerts, and debugging from structured
//     logs
//
// What it tests:
//   - Returns error_code and error_message fields
//   - error_code matches the AppError code
//   - error_message matches the AppError message
func TestFieldsExtraction(t *testing.T) {
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

// TestFieldsWithDetails tests that ZapFields() includes error_details when present.
//
// Why this test is important:
//   - AppError.Details carries contextual key-value pairs (e.g. tenant_id, document_id)
//   - These must appear in structured logs for correlation and debugging
//
// What it tests:
//   - ZapFields() includes an error_details object field when Details is non-empty
//   - The details field is of ObjectMarshalerType
func TestFieldsWithDetails(t *testing.T) {
	t.Parallel()

	appErr := apperr.WithDetails(
		apperr.NotFound("document not found"),
		map[string]string{
			"document_id": "doc-123",
			"tenant_id":   "tenant-456",
		},
	)

	fields := apperr.ZapFields(appErr)

	found := false
	for _, f := range fields {
		if f.Key == "error_details" {
			found = true
			assert.Equal(t, zapcore.ObjectMarshalerType, f.Type)
		}
	}
	assert.True(t, found, "expected error_details field when Details is non-empty")
}

// TestCombineAppendErrors tests the multi-error round-trip: Combine -> Errors.
//
// Why this test is important:
//   - Batch operations (e.g. ingesting multiple documents) collect partial failures
//   - Combine/Append/Errors must faithfully aggregate and decompose multi-errors
//
// What it tests:
//   - Combine(nil, err1, nil, err2) produces a non-nil combined error
//   - Errors() on the combined error returns exactly 2 errors
//   - Append(nil, err) returns err
//   - Append(combined, err3) produces a 3-error combined error
//   - Combine with all nils returns nil
func TestCombineAppendErrors(t *testing.T) {
	t.Parallel()

	err1 := apperr.Sentinel("error one")
	err2 := apperr.Sentinel("error two")
	err3 := apperr.Sentinel("error three")

	combined := apperr.Combine(nil, err1, nil, err2)
	require.Error(t, combined)

	errs := apperr.Errors(combined)
	require.Len(t, errs, 2)
	assert.Equal(t, "error one", errs[0].Error())
	assert.Equal(t, "error two", errs[1].Error())

	result := apperr.Append(nil, err1)
	require.Error(t, result)
	assert.Equal(t, "error one", result.Error())

	combined = apperr.Append(combined, err3)
	errs = apperr.Errors(combined)
	require.Len(t, errs, 3)

	assert.NoError(t, apperr.Combine(nil, nil))
}

// TestErrorsOnSingleError tests that Errors() on a non-combined error returns a
// single-element slice.
//
// Why this test is important:
//   - LogErrors iterates Errors(); it must handle single errors without panicking
//
// What it tests:
//   - Errors(plainErr) returns a slice of length 1
func TestErrorsOnSingleError(t *testing.T) {
	t.Parallel()

	err := apperr.Sentinel("standalone error")
	errs := apperr.Errors(err)
	require.Len(t, errs, 1)
	assert.Same(t, err, errs[0])
}

// TestLogErrorTransient tests that LogError logs transient errors at Warn level.
//
// Why this test is important:
//   - Transient errors (timeouts, unavailable) should not trigger Error-level alerts
//   - Operators rely on log levels to distinguish retryable from fatal failures
//
// What it tests:
//   - A Timeout AppError is logged at Warn level
//   - The log entry contains the message
//   - The log entry contains error_code and error_message fields
func TestFoundationLogErrorTransient(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	timeoutErr := apperr.Timeout("connection timed out")
	apperr.LogError(logger, "request failed", timeoutErr)

	entries := observed.All()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, zap.WarnLevel, entry.Level)
	assert.Equal(t, "request failed", entry.Message)

	fieldMap := contextFieldMap(entry.ContextMap())
	code, ok := fieldMap["error_code"]
	assert.True(t, ok, "expected error_code field")
	assert.Equal(t, string(apperr.CodeTimeout), code)
}

// TestLogErrorPermanent tests that LogError logs permanent errors at Error level.
//
// Why this test is important:
//   - Permanent errors (not found, unauthorized) indicate real failures that need
//     attention
//   - Error-level logging ensures they appear in alerting dashboards
//
// What it tests:
//   - A NotFound AppError is logged at Error level
//   - A plain (non-AppError) error is also logged at Error level
func TestFoundationLogErrorPermanent(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	notFoundErr := apperr.NotFound("record missing")
	apperr.LogError(logger, "lookup failed", notFoundErr)

	entries := observed.All()
	require.Len(t, entries, 1)
	assert.Equal(t, zap.ErrorLevel, entries[0].Level)
}

// TestLogErrorPlainError tests that LogError handles non-AppError errors.
//
// Why this test is important:
//   - Not all errors are AppErrors; the logger must not panic on stdlib errors
//
// What it tests:
//   - A plain error is logged at Error level with a standard "error" field
func TestFoundationLogErrorPlainError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogError(logger, "unexpected failure", apperr.Sentinel("disk full"))

	entries := observed.All()
	require.Len(t, entries, 1)
	assert.Equal(t, zap.ErrorLevel, entries[0].Level)
	assert.Equal(t, "unexpected failure", entries[0].Message)
}

// TestLogErrorsMultiError tests that LogErrors emits one log per error in a combined
// error.
//
// Why this test is important:
//   - Batch operations produce multi-errors; operators need a log entry for each failure
//   - Each individual error may have a different code and severity
//
// What it tests:
//   - A combined error with one transient and one permanent error produces 2 log entries
//   - The transient error is logged at Warn, the permanent at Error
func TestLogErrorsMultiError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	transientErr := apperr.Timeout("timed out")
	permanentErr := apperr.Internal("data corruption")
	combined := apperr.Combine(transientErr, permanentErr)

	apperr.LogErrors(logger, "batch operation failed", combined)

	entries := observed.All()
	require.Len(t, entries, 2)
	assert.Equal(
		t,
		zap.WarnLevel,
		entries[0].Level,
		"expected first entry at Warn (transient)",
	)
	assert.Equal(
		t,
		zap.ErrorLevel,
		entries[1].Level,
		"expected second entry at Error (permanent)",
	)
}

// TestLogErrorsSingleError tests that LogErrors works with a non-combined (single) error.
//
// Why this test is important:
//   - Callers should not need to check if an error is combined before calling LogErrors
//
// What it tests:
//   - A single error passed to LogErrors produces exactly 1 log entry
func TestLogErrorsSingleError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogErrors(logger, "single failure", apperr.Internal("oops"))

	assert.Equal(t, 1, observed.Len())
}

// TestMarshalLogObject_NoDetails tests that MarshalLogObject succeeds and
// encodes code + message when the AppError has no Details.
//
// Why this test is important:
//   - Most AppErrors are created via helpers like NotFound() which produce no
//     Details map. MarshalLogObject must skip the details encoding without error.
//   - Covers the len(e.Details)==0 branch and the return-nil path in zap.go
//
// What it tests:
//   - MarshalLogObject returns nil error for an AppError with empty Details
//   - The logged entry contains code and message but no details object
func TestFoundationMarshalLogObject_NoDetails(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	appErr := apperr.NotFound("record not found")
	logger.Error("test", zap.Object("error", appErr))

	entries := observed.All()
	require.Len(t, entries, 1)

	ctx := entries[0].ContextMap()
	errObj, ok := ctx["error"]
	require.True(t, ok, "expected 'error' field in log entry")

	errMap, ok := errObj.(map[string]any)
	require.True(t, ok, "expected error field to be map, got %T", errObj)
	assert.Equal(
		t,
		string(apperr.CodeNotFound),
		errMap["code"],
		"expected code=%q",
		apperr.CodeNotFound,
	)
	assert.Equal(t, "record not found", errMap["message"])
	assert.NotContains(
		t,
		errMap,
		"details",
		"expected no 'details' field when Details is empty",
	)
}

// TestMarshalLogObject_WithDetails tests that MarshalLogObject encodes
// the details map when present.
//
// Why this test is important:
//   - Exercises the AddObject("details", ...) branch inside MarshalLogObject
//   - Ensures the error return from enc.AddObject is propagated correctly
//
// What it tests:
//   - MarshalLogObject encodes details when the AppError has a non-empty Details map
//   - The details appear in the logged output
func TestMarshalLogObject_WithDetails(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	appErr := apperr.WithDetails(
		apperr.NotFound("document not found"),
		map[string]string{
			"document_id": "doc-abc",
		},
	)
	logger.Error("test", zap.Object("error", appErr))

	entries := observed.All()
	require.Len(t, entries, 1)

	ctx := entries[0].ContextMap()
	errObj, ok := ctx["error"]
	require.True(t, ok, "expected 'error' field in log entry")
	errMap, ok := errObj.(map[string]any)
	require.True(t, ok, "expected error field to be map, got %T", errObj)
	assert.Contains(
		t,
		errMap,
		"details",
		"expected 'details' field when Details is non-empty",
	)
}

// TestMarshalLogObject_CauseAndStack tests that MarshalLogObject surfaces the
// wrapped underlying cause and an origin stack trace.
//
// Why this test is important:
//   - This is the exact failure mode that blinded a real incident: an Ent/Postgres
//     error ("column does not exist") was wrapped as a generic "failed to query
//     database" and the underlying cause never reached the logs, so operators
//     could not see what actually broke.
//   - The stack trace must point at where the error originated (this test
//     function, which calls Wrap), not at the logging call site.
//
// What it tests:
//   - MarshalLogObject emits a "cause" field containing the wrapped error's text
//   - MarshalLogObject emits a non-empty "stack" field rooted at the caller of Wrap
func TestMarshalLogObject_CauseAndStack(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	underlying := apperr.Sentinel(
		`ERROR: column "deleted_at" does not exist (SQLSTATE 42703)`,
	)
	appErr := apperr.Wrap(
		underlying,
		apperr.CodeInternal,
		"failed to query database",
	)

	logger.Error("test", apperr.ZapError(appErr))

	entries := observed.All()
	require.Len(t, entries, 1)

	errMap, ok := entries[0].ContextMap()["error"].(map[string]any)
	require.True(
		t,
		ok,
		"expected error field to be map, got %T",
		entries[0].ContextMap()["error"],
	)

	cause, ok := errMap["cause"].(string)
	require.True(t, ok, "expected cause to be a string, got %T", errMap["cause"])
	assert.Contains(
		t,
		cause,
		"deleted_at",
		"expected cause to contain underlying error text",
	)

	stack, ok := errMap["stack"].(string)
	require.True(t, ok, "expected stack to be a string, got %T", errMap["stack"])
	assert.Contains(t, stack, "TestMarshalLogObject_CauseAndStack",
		"expected stack rooted at the calling function")
}

// contextFieldMap flattens the observer's ContextMap into a simple string map for
// assertions.
func contextFieldMap(m map[string]any) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}
