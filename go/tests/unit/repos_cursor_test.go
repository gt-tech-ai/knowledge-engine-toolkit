package unit_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
)

// TestCursorCodec_RoundTripAndUTCNormalization tests that a (createdAt, id) tuple
// survives an Encode → Decode round trip and that the timestamp is normalized to
// UTC in the process.
//
// Why this test is important:
//   - The cursor codec is the wire format for keyset pagination; if Encode/Decode
//     were not inverses, every "next page" request would resume at the wrong row,
//     silently skipping or repeating records. The UTC normalization guarantees a
//     cursor minted in one timezone decodes to the same instant everywhere.
//
// What it tests:
//   - Decode(Encode(ts, id)) yields the same id and the same instant, expressed in
//     UTC, for a timestamp that started in a non-UTC location.
func TestCursorCodec_RoundTripAndUTCNormalization(t *testing.T) {
	t.Parallel()

	codec := repository.NewCursorCodec()
	require.NotNil(t, codec)

	loc := time.FixedZone("UTC+5", 5*60*60)
	created := time.Date(2026, 7, 24, 12, 30, 45, 123456789, loc)

	token := codec.Encode(created, "id-42")
	require.NotEmpty(t, token)

	got, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, "id-42", got.ID)
	assert.True(
		t,
		created.Equal(got.CreatedAt),
		"decoded instant must equal the encoded instant",
	)
	assert.Equal(t, time.UTC, got.CreatedAt.Location(), "decoded timestamp is UTC")
}

// TestCursorCodec_EmptyID tests that a cursor with an empty id round-trips (empty
// ids are the natural first-page sentinel).
//
// Why this test is important:
//   - The first page is often requested with an empty tiebreaker id; the codec must
//     preserve it rather than corrupt the token.
//
// What it tests:
//   - Encode with an empty id decodes back to an empty id and the same instant.
func TestCursorCodec_EmptyID(t *testing.T) {
	t.Parallel()

	codec := repository.NewCursorCodec()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := codec.Decode(codec.Encode(created, ""))
	require.NoError(t, err)
	assert.Empty(t, got.ID)
	assert.True(t, created.Equal(got.CreatedAt))
}

// TestCursorCodec_DecodeInvalid tests that Decode rejects every malformed token
// shape with a CodeInvalidInput error rather than returning a bogus payload.
//
// Why this test is important:
//   - Cursors are opaque, client-supplied tokens; a tampered or truncated cursor
//     must surface as invalid input (a 4xx), never as a silently-wrong resume point
//     or an internal error.
//
// What it tests:
//   - Non-base64 input, a well-formed base64 payload missing the "|" separator, and
//     a payload whose timestamp segment is unparseable each return CodeInvalidInput.
func TestCursorCodec_DecodeInvalid(t *testing.T) {
	t.Parallel()

	codec := repository.NewCursorCodec()

	cases := []struct {
		name  string
		token string
	}{
		{
			name:  "not base64",
			token: "!!! not valid base64 !!!",
		},
		{
			name:  "missing separator",
			token: base64.RawURLEncoding.EncodeToString([]byte("no-separator-here")),
		},
		{
			name:  "unparseable timestamp",
			token: base64.RawURLEncoding.EncodeToString([]byte("not-a-timestamp|id-1")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := codec.Decode(tc.token)
			require.Error(t, err)
			assert.True(
				t,
				apperr.Is(err, apperr.CodeInvalidInput),
				"malformed cursor must map to CodeInvalidInput, got %v",
				err,
			)
		})
	}
}
