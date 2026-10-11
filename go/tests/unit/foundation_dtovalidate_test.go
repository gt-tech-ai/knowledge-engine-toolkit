package unit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/dtovalidate"
)

// TestDtovalidate_LeafRules tests the leaf rule library the generated DTO Validate()
// methods compose
//
// Why this test is important:
//   - these are the primitives every generated DTO check calls; a wrong bound or a
//     mis-reused stdlib call would silently pass bad input or reject good input across
//     every DTO at once.
//
// What it tests:
//   - each rule accepts a valid value and rejects the boundary violation, and the string
//     rules reuse stdlib (uuid/url/filepath) rather than a hand-rolled pattern.
func TestDtovalidate_LeafRules(t *testing.T) {
	require.NoError(t, dtovalidate.UUID("id", "11111111-1111-1111-1111-111111111111"))
	require.Error(t, dtovalidate.UUID("id", "not-a-uuid"))

	require.NoError(t, dtovalidate.Filename("f", "report.pdf", 512))
	require.Error(t, dtovalidate.Filename("f", "", 512))              // empty
	require.Error(t, dtovalidate.Filename("f", "../etc/passwd", 512)) // traversal
	require.Error(
		t,
		dtovalidate.Filename("f", "my..report.pdf", 512),
	) // any ".." substring (proto CEL parity)
	require.Error(t, dtovalidate.Filename("f", "sub/dir.txt", 512)) // separator
	require.Error(t, dtovalidate.Filename("f", "bad\x00.pdf", 512)) // control char
	require.Error(t, dtovalidate.Filename("f", strings.Repeat("a", 513), 512))
	require.NoError(
		t,
		dtovalidate.Filename("f", strings.Repeat("a", 999)+".pdf", 0),
	) // maxBytes<=0 => no byte cap

	require.NoError(t, dtovalidate.HTTPSURL("u", "https://cdn.example/logo.png"))
	require.Error(t, dtovalidate.HTTPSURL("u", "javascript:alert(1)"))
	require.Error(t, dtovalidate.HTTPSURL("u", "data:text/html,x"))
	require.Error(t, dtovalidate.HTTPSURL("u", "https://")) // host-less

	require.NoError(t, dtovalidate.Email("e", "a@b.com"))
	require.Error(t, dtovalidate.Email("e", "nope"))
	require.Error(
		t,
		dtovalidate.Email("e", "Name <a@b.com>"),
	) // display-name form rejected

	require.NoError(t, dtovalidate.IntRange("n", 5, 1, 10))
	require.Error(t, dtovalidate.IntRange("n", 0, 1, 10))
	require.Error(t, dtovalidate.IntRange("n", 11, 1, 10))
	require.NoError(
		t,
		dtovalidate.IntRange("n", 99, 1, dtovalidate.NoUpperBound),
	) // no upper bound
	require.NoError(
		t,
		dtovalidate.IntRange("n", -5, dtovalidate.NoLowerBound, 10),
	) // no lower bound
	require.Error(
		t,
		dtovalidate.IntRange("n", 999, dtovalidate.NoLowerBound, 0),
	) // real hi=0 bound (lte:0)

	require.NoError(t, dtovalidate.MaxItems("xs", 3, 5))
	require.Error(t, dtovalidate.MaxItems("xs", 6, 5))

	require.NoError(t, dtovalidate.MinMaxLen("s", "abc", 1, 5))
	require.Error(t, dtovalidate.MinMaxLen("s", "", 1, 5))
	require.Error(t, dtovalidate.MinMaxLen("s", "toolong", 1, 5))

	require.NoError(t, dtovalidate.EnumDefined("k", 2, []int32{1, 2, 3}))
	require.Error(t, dtovalidate.EnumDefined("k", 0, []int32{1, 2, 3}))
}
