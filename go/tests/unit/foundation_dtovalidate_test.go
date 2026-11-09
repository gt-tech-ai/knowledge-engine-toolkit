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
	t.Parallel()

	require.NoError(t, dtovalidate.UUID(textID, "11111111-1111-1111-1111-111111111111"))
	require.Error(t, dtovalidate.UUID(textID, "not-a-uuid"))

	require.NoError(t, dtovalidate.Filename(textF, "report.pdf", maxBytes512))
	require.Error(t, dtovalidate.Filename(textF, "", maxBytes512)) // empty
	require.Error(
		t,
		dtovalidate.Filename(textF, "../etc/passwd", maxBytes512),
	) // traversal
	require.Error(
		t,
		dtovalidate.Filename(textF, "my..report.pdf", maxBytes512),
	) // any ".." substring (proto CEL parity)
	require.Error(t, dtovalidate.Filename(textF, "sub/dir.txt", maxBytes512)) // separator
	require.Error(
		t,
		dtovalidate.Filename(textF, "bad\x00.pdf", maxBytes512),
	) // control char
	require.Error(
		t,
		dtovalidate.Filename(textF, strings.Repeat(textA, count513), maxBytes512),
	)
	require.NoError(
		t,
		dtovalidate.Filename(textF, strings.Repeat(textA, 999)+".pdf", 0),
	) // maxBytes<=0 => no byte cap

	require.NoError(t, dtovalidate.HTTPSURL(textU, "https://cdn.example/logo.png"))
	require.Error(t, dtovalidate.HTTPSURL(textU, "javascript:alert(1)"))
	require.Error(t, dtovalidate.HTTPSURL(textU, "data:text/html,x"))
	require.Error(t, dtovalidate.HTTPSURL(textU, "https://")) // host-less

	require.NoError(t, dtovalidate.Email(textE, textABCom))
	require.Error(t, dtovalidate.Email(textE, textNope))
	require.Error(
		t,
		dtovalidate.Email(textE, "Name <a@b.com>"),
	) // display-name form rejected

	require.NoError(t, dtovalidate.IntRange(textN, intRangeArg5, 1, intRangeArg10))
	require.Error(t, dtovalidate.IntRange(textN, 0, 1, intRangeArg10))
	require.Error(t, dtovalidate.IntRange(textN, intRangeArg11, 1, intRangeArg10))
	require.NoError(
		t,
		dtovalidate.IntRange(textN, intRangeArg99, 1, dtovalidate.NoUpperBound),
	) // no upper bound
	require.NoError(
		t,
		dtovalidate.IntRange(textN, -5, dtovalidate.NoLowerBound, intRangeArg10),
	) // no lower bound
	require.Error(
		t,
		dtovalidate.IntRange(textN, intRangeArg999, dtovalidate.NoLowerBound, 0),
	) // real hi=0 bound (lte:0)

	require.NoError(t, dtovalidate.MaxItems("xs", maxItemsArg3, maxItems5))
	require.Error(t, dtovalidate.MaxItems("xs", maxItemsArg6, maxItems5))

	require.NoError(t, dtovalidate.MinMaxLen(textS, "abc", 1, maxLen5))
	require.Error(t, dtovalidate.MinMaxLen(textS, "", 1, maxLen5))
	require.Error(t, dtovalidate.MinMaxLen(textS, "toolong", 1, maxLen5))

	require.NoError(t, dtovalidate.EnumDefined(textK, enumDefinedArg2, []int32{1, 2, 3}))
	require.Error(t, dtovalidate.EnumDefined(textK, 0, []int32{1, 2, 3}))
}
