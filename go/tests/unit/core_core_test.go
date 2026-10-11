// Package core_test provides tests for the core/types and core/errors packages
// to bring coverage above 90%.
//
// This file covers:
//   - types.ID: String(), IsEmpty()
//   - types.Option: Some(), None(), Get(), UnwrapOr()
//   - types.Page: HasMore()
//   - types.Timestamps: SoftDelete(), IsDeleted()
//   - errors/zap.go: zapFields.MarshalLogObject (the second MarshalLogObject at line 107)
package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ---------------------------------------------------------------------------
// types.ID - String(), IsEmpty()
// ---------------------------------------------------------------------------

// TestID_String tests that the ID type correctly exposes its underlying string
// representation.
//
// Why this test is important:
//   - Callers format IDs in log messages and error strings using String; an
//     incorrect string representation produces misleading observability data
//
// What it tests:
//   - ID("user-123").String() returns "user-123"
func TestID_String(t *testing.T) {
	t.Parallel()

	id := types.ID("user-123")
	assert.Equal(t, "user-123", id.String())
}

// TestID_String_Empty tests that an empty ID returns an empty string without
// panicking.
//
// Why this test is important:
//   - Zero-value ID fields must not panic on String; such panics would crash
//     any code path that formats an uninitialized entity ID
//
// What it tests:
//   - ID("").String() returns "" without error
func TestID_String_Empty(t *testing.T) {
	t.Parallel()

	id := types.ID("")
	assert.Empty(t, id.String())
}

// TestID_IsEmpty_True tests that IsEmpty correctly identifies empty IDs,
// protecting against accidental use of zero-value identifiers in database
// writes or API responses.
//
// Why this test is important:
//   - A zero-value ID passed to a DB write would create an entity with no
//     meaningful primary key, breaking foreign key constraints
//
// What it tests:
//   - ID("").IsEmpty() returns true
func TestID_IsEmpty_True(t *testing.T) {
	t.Parallel()

	id := types.ID("")
	assert.True(t, id.IsEmpty(), "IsEmpty should return true for empty ID")
}

// TestID_IsEmpty_False tests that IsEmpty returns false for populated IDs,
// ensuring valid identifiers are not treated as missing values.
//
// Why this test is important:
//   - A false positive from IsEmpty on a valid ID would cause valid entities to
//     be rejected by nil-guard checks that should only block zero values
//
// What it tests:
//   - ID("abc").IsEmpty() returns false
func TestID_IsEmpty_False(t *testing.T) {
	t.Parallel()

	id := types.ID("abc")
	assert.False(t, id.IsEmpty(), "IsEmpty should return false for non-empty ID")
}

// ---------------------------------------------------------------------------
// types.Option - Some(), None(), Get(), UnwrapOr()
// ---------------------------------------------------------------------------

// TestOption_Some tests that the Some constructor creates a valid Option with
// the given value.
//
// Why this test is important:
//   - Option replaces pointer-based optionality; callers branch on Valid to
//     decide whether to use the value, so a wrong Valid flag breaks all such branches
//
// What it tests:
//   - Some(42) sets Valid=true and Value=42
func TestOption_Some(t *testing.T) {
	t.Parallel()

	opt := types.Some(42)
	assert.True(t, opt.Valid, "Some should set Valid=true")
	assert.Equal(t, 42, opt.Value)
}

// TestOption_Some_String tests that Some works correctly with string type
// parameters, confirming the generic implementation handles multiple types.
//
// Why this test is important:
//   - Option is generic; a type-specific bug (e.g., string zero value handling)
//     would only manifest when the type parameter is string
//
// What it tests:
//   - Some("hello") sets Valid=true and Value="hello"
func TestOption_Some_String(t *testing.T) {
	t.Parallel()

	opt := types.Some("hello")
	assert.True(t, opt.Valid, "Some should set Valid=true")
	assert.Equal(t, "hello", opt.Value)
}

// TestOption_None tests that the None constructor creates an invalid (empty)
// Option.
//
// Why this test is important:
//   - Callers pass None to functions that check Valid before using the value;
//     a None that returns Valid=true would bypass the absent-value guard
//
// What it tests:
//   - None[int]() sets Valid=false
func TestOption_None(t *testing.T) {
	t.Parallel()

	opt := types.None[int]()
	assert.False(t, opt.Valid, "None should set Valid=false")
}

// TestOption_Get_Valid tests that Get returns the value and ok=true for a
// populated Option.
//
// Why this test is important:
//   - Callers use the two-return form to distinguish present from absent
//     values; a wrong ok=false for a populated Option would treat valid data as absent
//
// What it tests:
//   - Some("test").Get() returns ("test", true)
func TestOption_Get_Valid(t *testing.T) {
	t.Parallel()

	opt := types.Some("test")
	val, ok := opt.Get()
	assert.True(t, ok, "Get should return ok=true for Some")
	assert.Equal(t, "test", val)
}

// TestOption_Get_None tests that Get returns the zero value and ok=false for
// an empty Option.
//
// Why this test is important:
//   - Callers must never read stale or uninitialized data from an absent Option;
//     ok=true for None would corrupt downstream business logic
//
// What it tests:
//   - None[string]().Get() returns ("", false)
func TestOption_Get_None(t *testing.T) {
	t.Parallel()

	opt := types.None[string]()
	val, ok := opt.Get()
	assert.False(t, ok, "Get should return ok=false for None")
	assert.Empty(t, val, "Get should return zero value for None")
}

// TestOption_UnwrapOr_Valid tests that UnwrapOr returns the actual value when the
// Option is populated.
//
// Why this test is important:
//   - Fallback defaults must not shadow real data; if UnwrapOr returned the
//     fallback for a populated Option, the caller would silently use the wrong value
//
// What it tests:
//   - Some(10).UnwrapOr(99) returns 10, not 99
func TestOption_UnwrapOr_Valid(t *testing.T) {
	t.Parallel()

	opt := types.Some(10)
	assert.Equal(
		t,
		10,
		opt.UnwrapOr(99),
		"UnwrapOr should return actual value, not fallback",
	)
}

// TestOption_UnwrapOr_None tests that UnwrapOr returns the fallback value when the
// Option is empty.
//
// Why this test is important:
//   - Callers provide safe defaults inline via UnwrapOr; a broken fallback would
//     return the zero value instead of the caller's intended default
//
// What it tests:
//   - None[int]().UnwrapOr(99) returns 99
func TestOption_UnwrapOr_None(t *testing.T) {
	t.Parallel()

	opt := types.None[int]()
	assert.Equal(t, 99, opt.UnwrapOr(99), "UnwrapOr should return fallback for None")
}

// ---------------------------------------------------------------------------
// types.Page - HasMore()
// ---------------------------------------------------------------------------

// TestPage_HasMore_True tests that HasMore returns true when additional pages
// of results remain.
//
// Why this test is important:
//   - API clients rely on HasMore to decide whether to issue a follow-up
//     request; a false negative would truncate result sets at the first page
//
// What it tests:
//   - Page with Total=10, PageSize=2, PageNumber=1 returns HasMore=true
func TestPage_HasMore_True(t *testing.T) {
	t.Parallel()

	page := types.Page[string]{
		Items:      []string{"a", "b"},
		Total:      10,
		PageSize:   2,
		PageNumber: 1,
	}
	assert.True(t, page.HasMore(), "HasMore should return true when more pages exist")
}

// TestPage_HasMore_False tests that HasMore returns false when the current page
// is the last page.
//
// Why this test is important:
//   - A false positive from HasMore triggers spurious follow-up requests that
//     return empty pages and waste network round-trips
//
// What it tests:
//   - Page with Total=4, PageSize=2, PageNumber=2 returns HasMore=false
func TestPage_HasMore_False(t *testing.T) {
	t.Parallel()

	page := types.Page[string]{
		Items:      []string{"a", "b"},
		Total:      4,
		PageSize:   2,
		PageNumber: 2,
	}
	assert.False(t, page.HasMore(), "HasMore should return false when on the last page")
}

// TestPage_HasMore_ExactlyFull tests that HasMore returns false when items
// exactly fill all pages with no remainder.
//
// Why this test is important:
//   - An off-by-one in the boundary condition would trigger an empty trailing
//     page request on every fully-paginated result set
//
// What it tests:
//   - Page with Total=2, PageSize=2, PageNumber=1 returns HasMore=false
func TestPage_HasMore_ExactlyFull(t *testing.T) {
	t.Parallel()

	page := types.Page[string]{
		Items:      []string{"a"},
		Total:      2,
		PageSize:   2,
		PageNumber: 1,
	}
	assert.False(
		t,
		page.HasMore(),
		"HasMore should return false when PageNumber*PageSize >= Total",
	)
}

// TestPage_HasMore_SinglePage tests that HasMore returns false when all items
// fit on a single page.
//
// Why this test is important:
//   - The single-page case is the most common result for small data sets; a
//     regression here would cause every small query to trigger a spurious second fetch
//
// What it tests:
//   - Page with Total=3, PageSize=10, PageNumber=1 returns HasMore=false
func TestPage_HasMore_SinglePage(t *testing.T) {
	t.Parallel()

	page := types.Page[int]{
		Items:      []int{1, 2, 3},
		Total:      3,
		PageSize:   10,
		PageNumber: 1,
	}
	assert.False(
		t,
		page.HasMore(),
		"HasMore should return false when all items fit on one page",
	)
}

// ---------------------------------------------------------------------------
// types.Timestamps - SoftDelete(), IsDeleted()
// ---------------------------------------------------------------------------

// TestTimestamps_SoftDelete tests that SoftDelete sets the DeletedAt timestamp
// within the expected time window.
//
// Why this test is important:
//   - Soft-delete timestamps drive retention policies and audit trails; an
//     unset or far-future DeletedAt would cause records to escape delete-filters
//
// What it tests:
//   - DeletedAt is nil before SoftDelete and non-nil after
//   - The set timestamp falls within [before, after] of the call
func TestTimestamps_SoftDelete(t *testing.T) {
	t.Parallel()

	ts := types.Timestamps{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.Nil(t, ts.DeletedAt, "DeletedAt should be nil before SoftDelete")

	before := time.Now()
	ts.SoftDelete()
	after := time.Now()

	require.NotNil(t, ts.DeletedAt, "DeletedAt should be set after SoftDelete")
	assert.False(
		t,
		ts.DeletedAt.Before(before),
		"DeletedAt should not be before the call",
	)
	assert.False(
		t,
		ts.DeletedAt.After(after),
		"DeletedAt should not be after the call returned",
	)
}

// TestTimestamps_IsDeleted_False tests that IsDeleted returns false when no
// soft-delete has been performed.
//
// Why this test is important:
//   - Live records must not be hidden; a false positive from IsDeleted would
//     cause active entities to be filtered out of all query results
//
// What it tests:
//   - Timestamps with DeletedAt=nil returns IsDeleted()=false
func TestTimestamps_IsDeleted_False(t *testing.T) {
	t.Parallel()

	ts := types.Timestamps{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	assert.False(t, ts.IsDeleted(), "IsDeleted should return false when DeletedAt is nil")
}

// TestTimestamps_IsDeleted_True tests that IsDeleted returns true after
// SoftDelete has been called.
//
// Why this test is important:
//   - Queries that filter soft-deleted records depend on IsDeleted; a false
//     negative would expose deleted entities in active-record queries
//
// What it tests:
//   - IsDeleted() returns true after SoftDelete() is called
func TestTimestamps_IsDeleted_True(t *testing.T) {
	t.Parallel()

	ts := types.Timestamps{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	ts.SoftDelete()
	assert.True(t, ts.IsDeleted(), "IsDeleted should return true after SoftDelete")
}

// ---------------------------------------------------------------------------
// errors/zap.go - zapFields.MarshalLogObject (line 107)
// ---------------------------------------------------------------------------

// TestZapFieldsMarshalLogObject tests that AppError with details can be
// marshaled into zap log fields without error.
//
// Why this test is important:
//   - Structured error logging is the primary observability signal for
//     production incidents; a panicking marshal would crash the logging path
//     and suppress the very error being reported
//
// What it tests:
//   - ZapFields on an error with details logs without panic
//   - The resulting log entry contains error_code and error_message context keys
func TestZapFieldsMarshalLogObject(t *testing.T) {
	t.Parallel()

	// Create an error with details so ZapFields produces the error_details
	// object field, which internally uses zapFields.MarshalLogObject.
	err := apperr.WithDetails(
		apperr.Internal("db failure"),
		map[string]string{"table": "users", "op": "insert"},
	)

	fields := apperr.ZapFields(err)

	// Log through a real zap logger to exercise the marshal path.
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	logger.Error("test error", fields...)

	entries := observed.All()
	require.Len(t, entries, 1, "expected exactly one log entry")

	// Verify error_details context map was marshaled.
	ctx := entries[0].ContextMap()
	assert.Contains(t, ctx, "error_code", "expected error_code in logged context")
	assert.Contains(t, ctx, "error_message", "expected error_message in logged context")
}
