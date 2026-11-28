package unit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// casHexHash is a valid 64-digit lowercase hex hash used across the cas type tests.
const casHexHash = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// casHexHashOther is a second valid hash, distinct from casHexHash.
const casHexHashOther = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"

// casHashLen is the byte length of a hash, for building a non-hex string of that size.
const casHashLen = 32

// casRefName is the ref name the cas tests move.
const casRefName = "pointer"

// casMustHash parses text or fails the test.
func casMustHash(t *testing.T, text string) types.Hash {
	t.Helper()

	hash, err := types.ParseHash(text)
	require.NoError(t, err)

	return hash
}

// TestCASHash_ParseRoundTripsString tests types.ParseHash and Hash.String.
//
// Why this test is important:
//   - A hash travels as text through logs, JSON bodies and database keys; a parse that
//     accepted a short or non-hex value would address a node that can never be found
//
// What it tests:
//   - String renders lowercase hex that ParseHash reads back; upper-case hex parses to
//     the same hash; odd length, non-hex and wrong length are CodeInvalidInput; the
//     zero hash reports IsZero
func TestCASHash_ParseRoundTripsString(t *testing.T) {
	t.Parallel()

	hash := casMustHash(t, casHexHash)
	assert.Equal(t, casHexHash, hash.String())
	assert.False(t, hash.IsZero())
	assert.True(t, types.Hash{}.IsZero())
	assert.Equal(t, hash, casMustHash(t, strings.ToUpper(casHexHash)))

	nonHex := strings.Repeat("zz", casHashLen)

	for _, bad := range []string{"", "abc", casHexHash[1:], casHexHash + "00", nonHex} {
		_, err := types.ParseHash(bad)
		require.Error(t, err, bad)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), bad)
	}
}

// TestCASHash_TextMarshalling tests Hash.MarshalText and UnmarshalText.
//
// Why this test is important:
//   - A hash held in a struct field must encode as its hex string in JSON; the default
//     array encoding would write 32 numbers and break every reader
//
// What it tests:
//   - MarshalText yields the hex form, UnmarshalText reads it back, and a bad value is
//     CodeInvalidInput
func TestCASHash_TextMarshalling(t *testing.T) {
	t.Parallel()

	hash := casMustHash(t, casHexHash)

	text, err := hash.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, casHexHash, string(text))

	var back types.Hash

	require.NoError(t, back.UnmarshalText(text))
	assert.Equal(t, hash, back)

	err = back.UnmarshalText([]byte("nope"))
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestCASDomain_Validate tests types.Domain.Validate.
//
// Why this test is important:
//   - The domain prefixes every hashed byte string and ends at a NUL; an empty domain
//     removes the separation and one holding a NUL could forge another domain's prefix
//
// What it tests:
//   - a normal domain is valid; the empty domain and one containing NUL are
//     CodeInvalidInput
func TestCASDomain_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, types.Domain("nlp-annotation/v1").Validate())

	for _, bad := range []types.Domain{"", "a\x00b"} {
		err := bad.Validate()
		require.Error(t, err, string(bad))
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), string(bad))
	}
}

// TestCASRefMove_Validate tests types.RefMove.Validate.
//
// Why this test is important:
//   - A compare-and-swap that accepts a no-op or an unaddressed move would report
//     success without changing which version readers see
//
// What it tests:
//   - a zero From (create) and a From-to-To move are valid; a zero To, From equal to
//     To, an empty scope and an empty name are CodeInvalidInput
func TestCASRefMove_Validate(t *testing.T) {
	t.Parallel()

	from := casMustHash(t, casHexHash)
	dest := casMustHash(t, casHexHashOther)
	key := types.RefKey{Scope: "org-1", Name: casRefName}

	create := types.RefMove{Key: key, From: types.Hash{}, To: dest}
	require.NoError(t, create.Validate())

	swap := types.RefMove{Key: key, From: from, To: dest}
	require.NoError(t, swap.Validate())

	for name, move := range map[string]types.RefMove{
		"zero to":    {Key: key, From: from, To: types.Hash{}},
		"from == to": {Key: key, From: dest, To: dest},
		"no scope":   {Key: types.RefKey{Scope: "", Name: casRefName}, From: from, To: dest},
		"no name":    {Key: types.RefKey{Scope: "org-1", Name: ""}, From: from, To: dest},
	} {
		err := move.Validate()
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}
