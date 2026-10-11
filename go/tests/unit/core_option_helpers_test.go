package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestOption_IsSomeIsNoneUnwrapOrElse tests the Option predicate + lazy-fallback
// accessors for both the present and absent cases.
//
// Why this test is important:
//   - Callers branch on IsSome/IsNone and use UnwrapOrElse to supply a computed
//     default; wrong results would silently substitute the fallback for a real value
//     (or vice versa).
//
// What it tests:
//   - Some: IsSome true, IsNone false, UnwrapOrElse returns the value (fallback not
//     called).
//   - None: IsSome false, IsNone true, UnwrapOrElse returns the computed fallback.
func TestOption_IsSomeIsNoneUnwrapOrElse(t *testing.T) {
	t.Parallel()

	some := types.Some(42)
	assert.True(t, some.IsSome())
	assert.False(t, some.IsNone())
	assert.Equal(t, 42, some.UnwrapOrElse(func() int {
		t.Fatal("fallback must not be called when a value is present")
		return -1
	}))

	none := types.None[int]()
	assert.False(t, none.IsSome())
	assert.True(t, none.IsNone())
	assert.Equal(t, -1, none.UnwrapOrElse(func() int { return -1 }))
}

// TestSomeIfNotEmpty tests the ""→None / non-empty→Some string-Option constructor.
//
// Why this test is important:
//   - It is the shared transport-edge mapper for optional proto string fields
//     (description, search filter); an empty string MUST become None (absent), not
//     Some("") — otherwise a "clear the field" partial update is indistinguishable from
//     "leave unchanged".
//
// What it tests:
//   - "" yields None; a non-empty string yields Some with that value.
func TestSomeIfNotEmpty(t *testing.T) {
	t.Parallel()

	assert.True(t, types.SomeIfNotEmpty("").IsNone())
	got := types.SomeIfNotEmpty("hello")
	assert.True(t, got.IsSome())
	assert.Equal(t, "hello", got.UnwrapOr(""))
}
