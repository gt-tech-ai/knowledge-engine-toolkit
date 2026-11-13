package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Literals for TestVersion_MeetsMinimumComparesThePatch.
const (
	// textVer1274 is a minimum that names a patch release.
	textVer1274 = "1.27.4"

	// textVer127 is a version written without a patch.
	textVer127 = "1.27"
)

// TestVersion_MeetsMinimumComparesThePatch tests that a minimum version's patch
// component is honored: a toolchain one patch behind the minimum is not accepted.
//
// Why this test is important:
//   - A module can require a patch release (`go 1.27.4` in go.work); with
//     GOTOOLCHAIN=local, Go 1.27.1 refuses to build it. A prerequisite check that
//     compares only major.minor passes 1.27.1 and the failure surfaces later as a
//     build error instead of an actionable "upgrade Go" message.
//
// What it tests:
//   - ParseVersion keeps the patch, and MeetsMinimum orders major, then minor,
//     then patch; a version without a patch reads as patch 0; a patch with a
//     non-numeric suffix ("4-beta") keeps its leading digits; String round-trips
//     the input form.
func TestVersion_MeetsMinimumComparesThePatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		found, minimum string
		want           bool
	}{
		{found: "1.27.1", minimum: textVer1274, want: false},
		{found: textVer1274, minimum: textVer1274, want: true},
		{found: "1.27.10", minimum: "1.27.9", want: true},
		{found: "1.28.0", minimum: textVer1274, want: true},
		{found: "1.26.9", minimum: textVer127, want: false},
		{found: textVer127, minimum: "1.27.0", want: true},
		{found: textVer127, minimum: "1.27.1", want: false},
		{found: "1.27.4-beta", minimum: textVer1274, want: true},
		{found: "2.0", minimum: "1.99.99", want: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.found+">="+testCase.minimum, func(t *testing.T) {
			t.Parallel()

			found, err := types.ParseVersion(testCase.found)
			require.NoError(t, err)

			minimum, err := types.ParseVersion(testCase.minimum)
			require.NoError(t, err)

			assert.Equal(t, testCase.want, found.MeetsMinimum(minimum))
		})
	}

	for _, text := range []string{textVer127, textVer1274, "v2.3.1"} {
		v, err := types.ParseVersion(text)
		require.NoError(t, err)
		assert.Equal(t, text[len(text)-len(v.String()):], v.String())
	}
}
