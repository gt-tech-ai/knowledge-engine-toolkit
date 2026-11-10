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

	require.NoError(t, dtovalidate.Filename(textF, "report.pdf", filenameInput))
	require.Error(t, dtovalidate.Filename(textF, "", filenameInput)) // empty
	require.Error(
		t,
		dtovalidate.Filename(textF, "../etc/passwd", filenameInput),
	) // traversal
	require.Error(
		t,
		dtovalidate.Filename(textF, "my..report.pdf", filenameInput),
	) // any ".." substring (proto CEL parity)
	require.Error(
		t,
		dtovalidate.Filename(textF, "sub/dir.txt", filenameInput),
	) // separator
	require.Error(
		t,
		dtovalidate.Filename(textF, "bad\x00.pdf", filenameInput),
	) // control char
	require.Error(
		t,
		dtovalidate.Filename(
			textF,
			strings.Repeat(textA, repeatInputRules),
			filenameInput,
		),
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

	testDtovalidateStringRules(t)
}

// testDtovalidateStringRules continues TestDtovalidate_LeafRules.
func testDtovalidateStringRules(t *testing.T) {
	t.Helper()

	require.Error(
		t,
		dtovalidate.Email(textE, "Name <a@b.com>"),
	) // display-name form rejected

	require.NoError(t, dtovalidate.IntRange(textN, intRangeInput, 1, intRangeInputRules))
	require.Error(t, dtovalidate.IntRange(textN, 0, 1, intRangeInputRules))
	require.Error(
		t,
		dtovalidate.IntRange(textN, intRangeInputLeafRules, 1, intRangeInputRules),
	)
	require.NoError(
		t,
		dtovalidate.IntRange(
			textN,
			intRangeInputDtovalidateLeafRules,
			1,
			dtovalidate.NoUpperBound,
		),
	) // no upper bound
	require.NoError(
		t,
		dtovalidate.IntRange(textN, -5, dtovalidate.NoLowerBound, intRangeInputRules),
	) // no lower bound
	require.Error(
		t,
		dtovalidate.IntRange(
			textN,
			intRangeInputTestDtovalidateLeafRules,
			dtovalidate.NoLowerBound,
			0,
		),
	) // real hi=0 bound (lte:0)

	require.NoError(t, dtovalidate.MaxItems("xs", maxItemsInput, maxItemsInputRules))
	require.Error(
		t,
		dtovalidate.MaxItems("xs", maxItemsInputLeafRules, maxItemsInputRules),
	)

	require.NoError(t, dtovalidate.MinMaxLen(textS, "abc", 1, minMaxLenInput))
	require.Error(t, dtovalidate.MinMaxLen(textS, "", 1, minMaxLenInput))
	require.Error(t, dtovalidate.MinMaxLen(textS, "toolong", 1, minMaxLenInput))

	require.NoError(t, dtovalidate.EnumDefined(textK, enumDefinedInput, []int32{1, 2, 3}))
	require.Error(t, dtovalidate.EnumDefined(textK, 0, []int32{1, 2, 3}))
}

const (
	// enumDefinedInput is the count passed to EnumDefined in testDtovalidateStringRules.
	enumDefinedInput = 2

	// filenameInput is the maxBytes passed to Filename in TestDtovalidate_LeafRules.
	filenameInput = 512

	// intRangeInput is the v passed to IntRange in testDtovalidateStringRules.
	intRangeInput = 5

	// intRangeInputDtovalidateLeafRules is the v passed to IntRange in
	// testDtovalidateStringRules.
	intRangeInputDtovalidateLeafRules = 99

	// intRangeInputLeafRules is the v passed to IntRange in testDtovalidateStringRules.
	intRangeInputLeafRules = 11

	// intRangeInputRules is the hi passed to IntRange in testDtovalidateStringRules.
	intRangeInputRules = 10

	// intRangeInputTestDtovalidateLeafRules is the v passed to IntRange in
	// testDtovalidateStringRules.
	intRangeInputTestDtovalidateLeafRules = 999

	// maxItemsInput is the n passed to MaxItems in testDtovalidateStringRules.
	maxItemsInput = 3

	// maxItemsInputLeafRules is the n passed to MaxItems in testDtovalidateStringRules.
	maxItemsInputLeafRules = 6

	// maxItemsInputRules is the maxItems passed to MaxItems in
	// testDtovalidateStringRules.
	maxItemsInputRules = 5

	// minMaxLenInput is the maxLen passed to MinMaxLen in testDtovalidateStringRules.
	minMaxLenInput = 5

	// repeatInputRules is the count passed to Repeat in TestDtovalidate_LeafRules.
	repeatInputRules = 513
)
