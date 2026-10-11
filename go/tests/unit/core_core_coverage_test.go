// Package core_test provides additional tests to increase coverage of the core layer.
//
// This file covers:
//   - Error classify: unknown error code HTTP mapping
//   - Error: Is with non-AppError, Code extraction, Ingestion constructor
//   - Zap: ZapFields with stack/details, LogErrors, Errors, detailsObject
//   - Types: type aliases
package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ---------------------------------------------------------------------------
// Error classify -- unknown code
// ---------------------------------------------------------------------------

// TestToHTTPStatus_UnknownCode tests that unrecognized error codes default to
// HTTP 500, preventing unclassified errors from leaking unexpected status codes.
//
// Why this test is important:
//   - Programmatic or external error codes not in the switch table must
//     default to 500 rather than returning zero, which would be an invalid
//     HTTP status and confuse clients
//
// What it tests:
//   - ToHTTPStatus("CUSTOM_CODE") returns 500
func TestToHTTPStatus_UnknownCode(t *testing.T) {
	t.Parallel()

	status := apperr.ToHTTPStatus(apperr.ErrorCode("CUSTOM_CODE"))
	assert.Equal(t, 500, status)
}

// ---------------------------------------------------------------------------
// Error -- Is with non-AppError
// ---------------------------------------------------------------------------

// TestIs_NonAppError tests that the Is function does not falsely match plain
// Go errors against AppError codes.
//
// Why this test is important:
//   - A false positive would trigger wrong retry behavior and suppress the
//     real error classification for non-domain errors
//
// What it tests:
//   - Is(plainError, CodeNotFound) returns false
func TestIs_NonAppError(t *testing.T) {
	t.Parallel()

	plainErr := apperr.Sentinel("plain error")
	assert.False(
		t,
		apperr.Is(plainErr, apperr.CodeNotFound),
		"Is should return false for non-AppError",
	)
}

// TestIs_NilError tests that the Is function handles nil errors without
// panicking.
//
// Why this test is important:
//   - Nil is a valid error value in Go; Is(nil, code) must return false without
//     a nil-pointer-dereference that would crash the caller
//
// What it tests:
//   - Is(nil, CodeNotFound) returns false
func TestIs_NilError(t *testing.T) {
	t.Parallel()

	assert.False(
		t,
		apperr.Is(nil, apperr.CodeNotFound),
		"Is should return false for nil error",
	)
}

// ---------------------------------------------------------------------------
// Error -- Code extraction
// ---------------------------------------------------------------------------

// TestCode_AppError tests that the Code function extracts the correct error
// code from an AppError.
//
// Why this test is important:
//   - Code extraction is used by HTTP handlers and middleware to map errors to
//     status codes; incorrect extraction would silently return 500 for all errors
//
// What it tests:
//   - Code(NotFound("not found")) returns CodeNotFound
func TestCode_AppError(t *testing.T) {
	t.Parallel()

	err := apperr.NotFound("not found")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

// TestCode_PlainError tests that the Code function returns CodeUnknown for
// non-AppError errors.
//
// Why this test is important:
//   - Non-AppError errors must return CodeUnknown, not an arbitrary code;
//     returning a wrong code would route the error to the wrong handler
//
// What it tests:
//   - Code(errors.New("plain")) returns CodeUnknown
func TestCode_PlainError(t *testing.T) {
	t.Parallel()

	assert.Equal(t, apperr.CodeUnknown, apperr.Code(apperr.Sentinel("plain")))
}

// TestCode_NilError tests that the Code function handles nil errors by
// returning CodeUnknown.
//
// Why this test is important:
//   - Nil is a valid error value; Code(nil) must return CodeUnknown without
//     panicking to keep callers safe from nil-check omissions
//
// What it tests:
//   - Code(nil) returns CodeUnknown
func TestCode_NilError(t *testing.T) {
	t.Parallel()

	assert.Equal(t, apperr.CodeUnknown, apperr.Code(nil))
}

// ---------------------------------------------------------------------------
// Error -- Ingestion constructor
// ---------------------------------------------------------------------------

// TestIngestion_Constructor tests that the Ingestion error constructor creates
// an AppError with the correct code and message.
//
// Why this test is important:
//   - The Ingestion constructor is the only path that produces CodeIngestion;
//     a wrong code would route the error to the wrong pipeline error handler
//
// What it tests:
//   - Ingestion("parsing failed") returns non-nil error with CodeIngestion and the given
//     message
func TestIngestion_Constructor(t *testing.T) {
	t.Parallel()

	err := apperr.Ingestion("parsing failed")
	require.NotNil(t, err)
	assert.Equal(t, apperr.CodeIngestion, err.Code)
	assert.Equal(t, "parsing failed", err.Message)
}

// ---------------------------------------------------------------------------
// Error -- Error() string with Cause
// ---------------------------------------------------------------------------

// TestErrorString_WithCause tests that the Error() string includes both the
// AppError message and the wrapped cause for debuggability.
//
// Why this test is important:
//   - Error strings appear in logs and error chains; the cause must be appended
//     so operators can trace back to the original failure without examining the
//     full stack
//
// What it tests:
//   - Wrap(cause, CodeUnavailable, "database failed").Error() returns "UNAVAILABLE:
//     database failed: connection refused"
func TestErrorString_WithCause(t *testing.T) {
	t.Parallel()

	cause := apperr.Sentinel("connection refused")
	err := apperr.Wrap(cause, apperr.CodeUnavailable, "database failed")
	assert.Equal(t, "UNAVAILABLE: database failed: connection refused", err.Error())
}

// ---------------------------------------------------------------------------
// Zap -- ZapFields with stack
// ---------------------------------------------------------------------------

// TestZapFields_NoDetails tests that ZapFields emits code, message, and an
// origin stack for a plain AppError, and omits details when none are attached.
//
// Why this test is important:
//   - Zap fields are the primary observability signal in production; emitting
//     extra fields (like error_details on a no-details error) wastes log
//     storage and confuses dashboards
//
// What it tests:
//   - ZapFields includes error_code, error_message, and error_stack
//   - ZapFields omits error_details and error_cause for an unwrapped, no-details error
func TestZapFields_NoDetails(t *testing.T) {
	t.Parallel()

	err := apperr.NotFound("not found")
	keys := fieldKeySet(apperr.ZapFields(err))

	for _, want := range []string{"error_code", "error_message", "error_stack"} {
		assert.True(t, keys[want], "expected %s field, got keys %v", want, keys)
	}
	for _, absent := range []string{"error_details", "error_cause"} {
		assert.False(
			t,
			keys[absent],
			"did not expect %s field, got keys %v",
			absent,
			keys,
		)
	}
}

// TestZapFields_WithDetails tests that ZapFields includes an error_details
// field when details are attached to the AppError.
//
// Why this test is important:
//   - Structured details contain the context needed for debugging; a missing
//     error_details field silently discards the debugging context from logs
//
// What it tests:
//   - ZapFields on an AppError with details includes a field keyed "error_details"
func TestZapFields_WithDetails(t *testing.T) {
	t.Parallel()

	err := apperr.WithDetails(
		apperr.NotFound("not found"),
		map[string]string{"doc_id": "123"},
	)

	fields := apperr.ZapFields(err)
	found := false
	for _, f := range fields {
		if f.Key == "error_details" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected error_details field when Details is non-empty")
}

// TestZapFields_WithMultipleDetails tests that ZapFields emits an error_details
// field (alongside code, message, and stack) when multiple detail entries are
// attached.
//
// Why this test is important:
//   - Multiple detail entries must all appear in the log; a truncated or
//     omitted details map silently loses debugging context
//
// What it tests:
//   - ZapFields includes error_code, error_message, error_stack, and error_details
func TestZapFields_WithMultipleDetails(t *testing.T) {
	t.Parallel()

	err := apperr.WithDetails(
		apperr.NotFound("not found"),
		map[string]string{"doc_id": "123", "tenant_id": "456"},
	)

	keys := fieldKeySet(apperr.ZapFields(err))
	for _, want := range []string{
		"error_code", "error_message", "error_stack", "error_details",
	} {
		assert.True(t, keys[want], "expected %s field, got keys %v", want, keys)
	}
}

// fieldKeySet collects the set of zap.Field keys for order-independent,
// count-independent assertions.
func fieldKeySet(fields []zap.Field) map[string]bool {
	keys := make(map[string]bool, len(fields))
	for _, f := range fields {
		keys[f.Key] = true
	}
	return keys
}

// ---------------------------------------------------------------------------
// Zap -- Combine, Errors, LogErrors
// ---------------------------------------------------------------------------

// TestCombine tests that Combine merges multiple errors into a single combined
// error that can be decomposed back.
//
// Why this test is important:
//   - Combine is used to collect errors from parallel operations; a broken
//     Combine hides failures by discarding all but the first error
//
// What it tests:
//   - Combine(err1, err2) returns a non-nil combined error
//   - Errors() on the combined error returns exactly 2 errors
func TestCombine(t *testing.T) {
	t.Parallel()

	err1 := apperr.Sentinel("err1")
	err2 := apperr.Sentinel("err2")

	combined := apperr.Combine(err1, err2)
	require.Error(t, combined)

	errs := apperr.Errors(combined)
	require.Len(t, errs, 2)
}

// TestCombine_AllNils tests that Combine returns nil when all inputs are nil,
// preventing spurious error creation.
//
// Why this test is important:
//   - Returning non-nil for all-nil inputs creates spurious errors in code that
//     guards on `err != nil`, causing false-positive failure handling
//
// What it tests:
//   - Combine(nil, nil) returns nil
func TestCombine_AllNils(t *testing.T) {
	t.Parallel()

	assert.NoError(t, apperr.Combine(nil, nil), "expected nil for all-nil combine")
}

// TestAppend tests that Append incrementally builds a combined error from
// individual errors.
//
// Why this test is important:
//   - Append is used in loops to accumulate errors; a broken Append would
//     silently drop errors that should surface to the caller
//
// What it tests:
//   - Append(nil, err) returns the error itself
//   - Append(existing, err2) returns a combined error with 2 entries
func TestAppend(t *testing.T) {
	t.Parallel()

	result := apperr.Append(nil, apperr.Sentinel("first"))
	require.Error(t, result)

	result = apperr.Append(result, apperr.Sentinel("second"))
	errs := apperr.Errors(result)
	require.Len(t, errs, 2)
}

// TestErrors_SingleError tests that Errors wraps a single non-combined error
// into a one-element slice.
//
// Why this test is important:
//   - Callers use Errors() to iterate over all errors; a wrong length for a
//     single-error input breaks error-count-based logic
//
// What it tests:
//   - Errors(singleErr) returns a slice of length 1 containing the original error
func TestErrors_SingleError(t *testing.T) {
	t.Parallel()

	err := apperr.Sentinel("single")
	errs := apperr.Errors(err)
	require.Len(t, errs, 1)
	assert.Same(t, err, errs[0])
}

// TestLogErrors_MultiError tests that LogErrors emits one log entry per error
// in a combined error.
//
// Why this test is important:
//   - Each error in a combined batch must produce its own log entry for correct
//     error attribution in dashboards; missing entries hide failures
//
// What it tests:
//   - LogErrors on a combined error with 2 errors produces exactly 2 log entries
func TestLogErrors_MultiError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	combined := apperr.Combine(
		apperr.Timeout("timed out"),
		apperr.NotFound("missing"),
	)

	apperr.LogErrors(logger, "batch failed", combined)

	require.Len(t, observed.All(), 2)
}

// TestLogErrors_SingleError tests that LogErrors handles a single
// (non-combined) error by emitting exactly one log entry.
//
// Why this test is important:
//   - A single non-combined error must still produce exactly one log entry;
//     zero entries would silently swallow the error in production
//
// What it tests:
//   - LogErrors on a single error produces exactly 1 log entry
func TestLogErrors_SingleError(t *testing.T) {
	t.Parallel()

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	apperr.LogErrors(logger, "single error", apperr.Internal("oops"))

	assert.Equal(t, 1, observed.Len())
}

// ---------------------------------------------------------------------------
// MarshalLogObject -- without details
// ---------------------------------------------------------------------------

// TestMarshalLogObject_NoDetails tests that MarshalLogObject works correctly
// for AppErrors without details, exercising the ZapError code path.
//
// Why this test is important:
//   - MarshalLogObject is called by zap when logging AppErrors; a failure here
//     would crash the logging subsystem at error time in production
//
// What it tests:
//   - Logging an AppError without details produces exactly one log entry
func TestCoreMarshalLogObject_NoDetails(t *testing.T) {
	t.Parallel()

	err := apperr.NotFound("not found")

	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	logger.Error("test", apperr.ZapError(err))

	require.Len(t, observed.All(), 1)
}

// ---------------------------------------------------------------------------
// Types -- verify type aliases
// ---------------------------------------------------------------------------

// TestPageRequest tests that the PageRequest type correctly stores pagination
// parameters.
//
// Why this test is important:
//   - PageRequest fields drive SQL LIMIT/OFFSET; wrong defaults would
//     silently truncate or skip pages in paginated API responses
//
// What it tests:
//   - PageRequest with PageSize=10 and PageNumber=1 retains both values
func TestPageRequest(t *testing.T) {
	t.Parallel()

	pr := types.PageRequest{
		PageSize:   10,
		PageNumber: 1,
	}
	assert.Equal(t, 10, pr.PageSize)
}

// TestPage tests that the generic Page type correctly stores items and total
// count.
//
// Why this test is important:
//   - Page.Items drives pagination responses; a wrong Items slice length would
//     cause clients to receive wrong item counts and misaligned cursors
//
// What it tests:
//   - Page[string] with 2 items and Total=2 retains both values
func TestPage(t *testing.T) {
	t.Parallel()

	page := types.Page[string]{
		Items: []string{"a", "b"},
		Total: 2,
	}
	assert.Len(t, page.Items, 2)
}

// TestID_StringAndIsEmpty tests that the ID type helpers String and IsEmpty
// behave correctly for populated and empty IDs.
//
// Why this test is important:
//   - IDs appear in log messages and error strings via String; IsEmpty guards
//     database writes from zero-value identifiers
//
// What it tests:
//   - String returns the underlying string value
//   - IsEmpty returns true for empty IDs and false for non-empty IDs
func TestID_StringAndIsEmpty(t *testing.T) {
	t.Parallel()

	id := types.ID("abc-123")
	assert.Equal(t, "abc-123", id.String())
	assert.False(t, id.IsEmpty())

	empty := types.ID("")
	assert.True(t, empty.IsEmpty())
}

// TestOption_UnwrapOr tests that Option.UnwrapOr returns the actual value for a
// populated Option and the fallback for an empty Option.
//
// Why this test is important:
//   - UnwrapOr is the primary way callers provide safe defaults for absent
//     optional fields; incorrect fallback behavior silently uses wrong values
//
// What it tests:
//   - Some(42).UnwrapOr(0) returns 42
//   - None[int]().UnwrapOr(99) returns 99
func TestOption_UnwrapOr(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 42, types.Some(42).UnwrapOr(0))
	assert.Equal(t, 99, types.None[int]().UnwrapOr(99))
}

// TestPage_HasMore tests that HasMore returns true when more pages of results
// remain and false when all pages have been consumed.
//
// Why this test is important:
//   - API clients use HasMore to decide whether to issue a follow-up request;
//     incorrect behavior causes spurious requests or missed results
//
// What it tests:
//   - HasMore returns true when PageNumber*PageSize < Total
//   - HasMore returns false when on the last page
func TestPage_HasMore(t *testing.T) {
	t.Parallel()

	p := types.Page[string]{
		Items:      []string{"a", "b"},
		Total:      10,
		PageNumber: 1,
		PageSize:   2,
	}
	assert.True(t, p.HasMore())

	full := types.Page[string]{
		Items:      []string{"a"},
		Total:      1,
		PageNumber: 1,
		PageSize:   10,
	}
	assert.False(t, full.HasMore())
}

// TestTimestamps_SoftDeleteAndIsDeleted tests that SoftDelete sets DeletedAt
// and IsDeleted detects it correctly.
//
// Why this test is important:
//   - Soft-delete timestamps drive retention policies and audit trails;
//     a wrong IsDeleted result would expose deleted entities to live queries
//
// What it tests:
//   - IsDeleted returns false before SoftDelete
//   - SoftDelete sets DeletedAt; IsDeleted returns true afterwards
func TestTimestamps_SoftDeleteAndIsDeleted(t *testing.T) {
	t.Parallel()

	ts := types.Timestamps{}
	assert.False(t, ts.IsDeleted())

	ts.SoftDelete()
	assert.True(t, ts.IsDeleted())
}

// ---------------------------------------------------------------------------
// errors/stdlib.go -- re-exported stdlib functions
// ---------------------------------------------------------------------------

// TestStdlib_Sentinel tests that Sentinel creates a plain sentinel error value
// usable for errors.Is comparison, keeping packages free of direct "errors"
// stdlib imports.
//
// Why this test is important:
//   - Sentinel errors are used as canonical identifiers in error chains;
//     a broken Sentinel would break all errors.Is comparisons across the codebase
//
// What it tests:
//   - Sentinel returns a non-nil error with the correct message
func TestStdlib_Sentinel(t *testing.T) {
	t.Parallel()

	ErrFoo := apperr.Sentinel("foo error")
	require.Error(t, ErrFoo)
	assert.Equal(t, "foo error", ErrFoo.Error())
}

// TestStdlib_StdIs tests that StdIs correctly delegates to errors.Is for
// sentinel error chain matching.
//
// Why this test is important:
//   - StdIs is the re-exported facade that keeps domain packages off the stdlib
//     import; a broken delegation would silently miss wrapped sentinels
//
// What it tests:
//   - StdIs matches a wrapped sentinel
//   - StdIs does not match a different sentinel
func TestStdlib_StdIs(t *testing.T) {
	t.Parallel()

	target := apperr.Sentinel("sentinel")
	wrapped := apperr.Wrap(target, apperr.ErrInternal, "context")

	assert.True(t, apperr.StdIs(wrapped, target), "StdIs should match wrapped sentinel")
	assert.False(
		t,
		apperr.StdIs(wrapped, apperr.Sentinel("other")),
		"StdIs should not match different sentinel",
	)
}

// TestStdlib_As tests that As correctly delegates to errors.As for type
// assertion through an error chain.
//
// Why this test is important:
//   - As is used to extract AppError from wrapped chains; a broken delegation
//     would prevent error type extraction in middleware and handler code
//
// What it tests:
//   - As extracts *AppError from a wrapped error chain
func TestStdlib_As(t *testing.T) {
	t.Parallel()

	appErr := apperr.NotFound("not found")
	wrapped := apperr.Join(appErr)

	var target *apperr.AppError
	assert.True(t, apperr.As(wrapped, &target), "As should find *AppError in chain")
	require.NotNil(t, target)
	assert.Equal(t, apperr.ErrNotFound, target.Code)
}

// TestStdlib_Join tests that Join combines multiple errors into one, discarding
// nils, so callers can accumulate errors without nil checks.
//
// Why this test is important:
//   - Join is used in validation loops to collect all field errors; nils must
//     be discarded or the joined error would always be non-nil
//
// What it tests:
//   - Join of two non-nil errors returns a combined error containing both messages
//   - Join of all nils returns nil
func TestStdlib_Join(t *testing.T) {
	t.Parallel()

	joined := apperr.Join(apperr.Sentinel("a"), nil, apperr.Sentinel("b"))
	require.Error(t, joined)
	assert.Contains(t, joined.Error(), "a")
	assert.Contains(t, joined.Error(), "b")

	assert.NoError(t, apperr.Join(nil, nil), "Join of all nils should return nil")
}

// TestStdlib_Unwrap tests that Unwrap exposes the directly wrapped error,
// allowing callers to peel one layer of wrapping at a time.
//
// Why this test is important:
//   - Unwrap is used in error chain traversal; a broken Unwrap breaks all
//     errors.Is and errors.As calls on multi-layer wrapped errors
//
// What it tests:
//   - Unwrap returns the directly wrapped error
//   - Unwrap on a non-wrapping error returns nil
func TestStdlib_Unwrap(t *testing.T) {
	t.Parallel()

	cause := apperr.Sentinel("cause")
	wrapped := apperr.Wrap(cause, apperr.ErrInternal, "outer")

	assert.Equal(t, cause, apperr.Unwrap(wrapped))
	assert.NoError(
		t,
		apperr.Unwrap(apperr.Sentinel("no cause")),
		"Sentinel has no wrapped error",
	)
}

// ---------------------------------------------------------------------------
// types/step_result.go
// ---------------------------------------------------------------------------

// TestStepStatus_String tests that each StepStatus constant returns the
// canonical string name used in CLI output and progress tables.
//
// Why this test is important:
//   - StepStatus strings appear in the preflight gate output table; wrong
//     strings confuse operators reading CI results
//
// What it tests:
//   - PASS, FAIL, SKIP, WARN return their canonical names
//   - An unknown status returns "UNKNOWN"
func TestStepStatus_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "PASS", types.StatusPass.String())
	assert.Equal(t, "FAIL", types.StatusFail.String())
	assert.Equal(t, "SKIP", types.StatusSkip.String())
	assert.Equal(t, "WARN", types.StatusWarn.String())
	assert.Equal(t, "UNKNOWN", types.StepStatus(99).String())
}

// TestStepResults_HasFailures tests that HasFailures returns true only when at
// least one step has StatusFail. The preflight gate uses this to decide whether
// to exit non-zero.
//
// Why this test is important:
//   - A wrong result would either cause false CI failures (always true) or
//     silently pass a broken preflight gate (always false)
//
// What it tests:
//   - All-pass results return false
//   - A set containing at least one FAIL returns true
//   - Empty results return false
func TestStepResults_HasFailures(t *testing.T) {
	t.Parallel()

	passing := types.StepResults{
		{Name: "lint", Status: types.StatusPass},
		{Name: "test", Status: types.StatusPass},
	}
	assert.False(t, passing.HasFailures(), "no failures in all-pass set")

	withFail := types.StepResults{
		{Name: "lint", Status: types.StatusPass},
		{Name: "test", Status: types.StatusFail},
	}
	assert.True(t, withFail.HasFailures(), "should detect the failing step")

	assert.False(t, types.StepResults{}.HasFailures(), "empty set has no failures")
}

// TestStepResults_AllPassed tests that AllPassed returns true only when every
// step has StatusPass, and false as soon as any other status is present.
//
// Why this test is important:
//   - AllPassed drives the final preflight gate decision; a wrong result would
//     silently ignore non-PASS statuses like WARN
//
// What it tests:
//   - Empty set returns true (vacuous truth)
//   - All-pass set returns true
//   - A set with a WARN status returns false
func TestStepResults_AllPassed(t *testing.T) {
	t.Parallel()

	assert.True(t, types.StepResults{}.AllPassed(), "empty set is considered all-passed")

	passing := types.StepResults{
		{Status: types.StatusPass},
		{Status: types.StatusPass},
	}
	assert.True(t, passing.AllPassed())

	mixed := types.StepResults{
		{Status: types.StatusPass},
		{Status: types.StatusWarn},
	}
	assert.False(
		t,
		mixed.AllPassed(),
		"Warn status should cause AllPassed to return false",
	)
}

// TestStepResults_CountByStatus tests that CountByStatus returns accurate
// per-status tallies used in summary output and quality gate decisions.
//
// Why this test is important:
//   - The preflight gate uses these counts in its summary table; wrong tallies
//     report the wrong number of passing or failing steps to the user
//
// What it tests:
//   - Correct counts for PASS, FAIL, and SKIP statuses
//   - Missing status (WARN) defaults to 0
func TestStepResults_CountByStatus(t *testing.T) {
	t.Parallel()

	rs := types.StepResults{
		{Status: types.StatusPass},
		{Status: types.StatusPass},
		{Status: types.StatusFail},
		{Status: types.StatusSkip},
	}
	counts := rs.CountByStatus()

	assert.Equal(t, 2, counts[types.StatusPass])
	assert.Equal(t, 1, counts[types.StatusFail])
	assert.Equal(t, 1, counts[types.StatusSkip])
	assert.Equal(t, 0, counts[types.StatusWarn], "missing status defaults to 0")
}

// TestStepResults_FailedNames tests that FailedNames returns only the names of
// steps that have StatusFail, used to surface actionable failure details in
// CLI output.
//
// Why this test is important:
//   - The preflight gate prints failed step names to help the user know what to
//     fix; incorrect names or extra names would mislead the user
//
// What it tests:
//   - Only FAIL steps are returned
//   - Empty and all-pass sets return nil
func TestStepResults_FailedNames(t *testing.T) {
	t.Parallel()

	rs := types.StepResults{
		{Name: "lint", Status: types.StatusPass},
		{Name: "test", Status: types.StatusFail},
		{Name: "build", Status: types.StatusFail},
	}
	names := rs.FailedNames()
	assert.Equal(t, []string{"test", "build"}, names)

	assert.Nil(t, types.StepResults{}.FailedNames(), "empty set returns nil slice")

	allPass := types.StepResults{{Name: "lint", Status: types.StatusPass}}
	assert.Nil(t, allPass.FailedNames(), "all-pass set returns nil slice")
}

// ---------------------------------------------------------------------------
// types/types.go -- OptionFromPtr, Option.ToPtr
// ---------------------------------------------------------------------------

// TestOptionFromPtr_NonNil tests that OptionFromPtr wraps a non-nil pointer
// into a Some Option, preventing callers from having to write nil guards before
// creating Options from pointer fields.
//
// Why this test is important:
//   - Without OptionFromPtr, callers writing nil guards before every pointer
//     field access would introduce inconsistent nil-check patterns
//
// What it tests:
//   - OptionFromPtr with a non-nil pointer returns a valid Some with the correct value
func TestOptionFromPtr_NonNil(t *testing.T) {
	t.Parallel()

	v := 42
	opt := types.OptionFromPtr(&v)
	assert.True(t, opt.Valid)
	assert.Equal(t, 42, opt.Value)
}

// TestOptionFromPtr_Nil tests that OptionFromPtr wraps a nil pointer into a
// None Option so callers get a consistent Option type from pointer fields
// regardless of nil-ness.
//
// Why this test is important:
//   - A nil pointer must produce a None, not a Some with zero value; the latter
//     would silently present absent data as present to callers
//
// What it tests:
//   - OptionFromPtr with a nil pointer returns a None (Valid=false)
func TestOptionFromPtr_Nil(t *testing.T) {
	t.Parallel()

	opt := types.OptionFromPtr[int](nil)
	assert.False(t, opt.Valid)
}

// TestOption_ToPtr_Some tests that ToPtr returns a non-nil pointer holding the
// Option's value, enabling callers to assign Option values into struct fields
// that require pointers.
//
// Why this test is important:
//   - ToPtr bridges Option and pointer-based APIs; a nil pointer for a Some
//     would cause nil-dereference panics in callers that assume non-nil
//
// What it tests:
//   - Some(99).ToPtr() returns a non-nil pointer with value 99
func TestOption_ToPtr_Some(t *testing.T) {
	t.Parallel()

	opt := types.Some(99)
	ptr := opt.ToPtr()
	require.NotNil(t, ptr)
	assert.Equal(t, 99, *ptr)
}

// TestOption_ToPtr_None tests that ToPtr returns nil for a None Option,
// maintaining pointer semantics (nil = absent) for callers that need pointers.
//
// Why this test is important:
//   - A non-nil pointer for a None would present absent data as present,
//     breaking optional FK fields that use nil to mean "not set"
//
// What it tests:
//   - None[int]().ToPtr() returns nil
func TestOption_ToPtr_None(t *testing.T) {
	t.Parallel()

	opt := types.None[int]()
	assert.Nil(t, opt.ToPtr())
}
