package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/helpers"
)

// TestFileExists tests that FileExists is true only for an existing regular
// file, distinguishing it from a missing path and from a directory.
//
// Why this test is important:
//   - Callers gate behavior on "is this a file" (e.g. config presence); treating
//     a directory or absent path as a file would mislead those decisions.
//
// What it tests:
//   - Missing path → false, after writing the file → true, directory path → false.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	require.False(t, helpers.FileExists(path), "expected false for non-existent file")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	require.True(t, helpers.FileExists(path), "expected true for existing file")
	// directory should not count as a file
	require.False(t, helpers.FileExists(dir), "expected false for directory path")
}

// TestDirExists tests that DirExists is true for an existing directory and false
// for a missing path.
//
// Why this test is important:
//   - Directory checks guard creation/lookup paths; a false positive on a missing
//     dir would skip a needed mkdir, a false negative would re-create it.
//
// What it tests:
//   - An existing temp dir → true; a non-existent child path → false.
func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	require.True(t, helpers.DirExists(dir), "expected true for existing dir")
	require.False(
		t,
		helpers.DirExists(filepath.Join(dir, "nope")),
		"expected false for non-existent dir",
	)
}

// TestOrDefault tests that OrDefault returns the env var when set and the
// fallback when it is unset.
//
// Why this test is important:
//   - This is the config-override primitive; getting the precedence backwards
//     would either ignore operator-set env vars or apply defaults over them.
//
// What it tests:
//   - A set key returns its value; an unset key returns the fallback.
func TestOrDefault(t *testing.T) {
	t.Setenv("HELPERS_TEST_KEY", "set-value")
	require.Equal(t, "set-value", helpers.OrDefault("HELPERS_TEST_KEY", "fallback"))
	require.Equal(t, "fallback", helpers.OrDefault("HELPERS_TEST_UNSET", "fallback"))
}

// TestUnique tests that Unique collapses duplicate elements to their first
// occurrence.
//
// Why this test is important:
//   - Used to de-duplicate module/path lists before fanning out work; duplicates
//     would cause the same command to run more than once.
//
// What it tests:
//   - A slice with repeats reduces to the count of distinct elements.
func TestUnique(t *testing.T) {
	got := helpers.Unique([]string{"a", "b", "a", "c", "b"})
	require.Len(t, got, 3)
}

// TestUniqueDirs tests that UniqueDirs reduces a set of file paths to their
// distinct parent directories.
//
// Why this test is important:
//   - Tools that operate per-directory (e.g. running a command once per package
//     dir) rely on this; counting a dir twice would duplicate that work.
//
// What it tests:
//   - Three paths sharing two parent dirs collapse to two unique directories.
func TestUniqueDirs(t *testing.T) {
	got := helpers.UniqueDirs([]string{"/a/b/c.go", "/a/b/d.go", "/x/y.go"})
	require.Len(t, got, 2)
}

// TestTruncate tests that Truncate caps a string at the rune limit and leaves
// shorter or empty strings unchanged.
//
// Why this test is important:
//   - Used to bound displayed output; operating on runes (not bytes) and not
//     over-trimming short strings avoids corrupting multibyte text or clipping
//     content that already fits.
//
// What it tests:
//   - An over-length string is cut to the limit; a shorter string and the empty
//     string pass through unchanged.
func TestTruncate(t *testing.T) {
	require.Equal(t, "hello", helpers.Truncate("hello world", 5))
	require.Equal(t, "hi", helpers.Truncate("hi", 10))
	require.Empty(t, helpers.Truncate("", 5))
}

// TestIsAllDigits tests that IsAllDigits recognises non-empty all-ASCII-digit
// strings and rejects everything else.
//
// Why this test is important:
//   - Used to classify high-cardinality id segments (e.g. numeric ids in request
//     paths) for metrics-label normalization; a false positive collapses a real
//     route segment, a false negative leaks cardinality.
//
// What it tests:
//   - Digit strings → true; empty, alphabetic, mixed, and signed inputs → false.
func TestIsAllDigits(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"42", true},
		{"0", true},
		{"", false},
		{"v1", false},
		{"12a", false},
		{"-3", false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, helpers.IsAllDigits(c.in), "IsAllDigits(%q)", c.in)
	}
}

// TestIsHex tests that IsHex recognises non-empty all-ASCII-hex strings.
//
// Why this test is important:
//   - Long hex tokens (object keys / hashes) are treated as ids for metrics-label
//     normalization; misclassifying a non-hex token would either leak cardinality
//     or collapse a legitimate route word.
//
// What it tests:
//   - Lower/upper hex and digits → true; empty and non-hex letters → false.
func TestIsHex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"deadBEEF01", true},
		{"0123456789abcdefABCDEF", true},
		{"", false},
		{"xyz", false},
		{"deadbeeg", false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, helpers.IsHex(c.in), "IsHex(%q)", c.in)
	}
}

// TestIsUUID tests that IsUUID recognises canonical 8-4-4-4-12 hyphenated UUIDs.
//
// Why this test is important:
//   - UUID path segments are collapsed to "{id}" for metrics-label normalization;
//     the classifier must accept the canonical form and reject look-alikes (wrong
//     length, wrong separators, non-hex bytes) to avoid mislabeling routes.
//
// What it tests:
//   - A canonical UUID → true; wrong length, misplaced separators, and non-hex
//     bytes → false.
func TestIsUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"11111111-1111-1111-1111-111111111111", true},
		{"", false},
		{"11111111-1111-1111-1111-11111111111", false},  // 35 chars
		{"111111111111-1-1111-1111-11111111111", false}, // misplaced separators
		{"gggggggg-gggg-gggg-gggg-gggggggggggg", false}, // non-hex
	}
	for _, c := range cases {
		require.Equal(t, c.want, helpers.IsUUID(c.in), "IsUUID(%q)", c.in)
	}
}
