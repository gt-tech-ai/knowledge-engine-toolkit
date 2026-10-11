package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TestPageToken_RoundTrip tests the opaque page-token codec.
//
// Why this test is important:
//   - The token is the ONLY thing a client round-trips to page a list, and it is opaque
//     bytes (no proto). If Encode→Decode lost the offset page, the keyset id, the
//     fingerprint, or the sort values, pagination would silently reset or seek the wrong
//     rows. An empty token must default to page 1, and a garbage token must be a clean
//     InvalidInput (reset-to-page-1), never a panic/500.
//
// What it tests:
//   - An offset token round-trips its page number (keyset arm absent).
//   - A keyset token round-trips its id + fingerprint; numeric sort values come back as
//     JSON numbers (float64), which is the contract Run relies on when restoring
//     timestamps.
//   - An empty token decodes to offset page 1; a malformed token is a CodeInvalidInput
//     error.
func TestPageToken_RoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("offset token round-trips the page number", func(t *testing.T) {
		t.Parallel()
		enc, err := listquery.EncodeToken(types.PageToken{OffsetPage: 7})
		require.NoError(t, err)
		got, err := listquery.DecodeToken(enc)
		require.NoError(t, err)
		assert.Nil(t, got.Keyset)
		assert.Equal(t, int32(7), got.OffsetPage)
	})

	t.Run(
		"keyset token round-trips id, fingerprint, and sort values",
		func(t *testing.T) {
			t.Parallel()
			enc, err := listquery.EncodeToken(types.PageToken{Keyset: &types.KeysetCursor{
				SortValues:  []any{int64(1_700_000_000_000_000), "indexed"},
				ID:          "11111111-2222-3333-4444-555555555555",
				Fingerprint: []byte{0xde, 0xad, 0xbe, 0xef},
			}})
			require.NoError(t, err)
			got, err := listquery.DecodeToken(enc)
			require.NoError(t, err)
			require.NotNil(t, got.Keyset)
			assert.Equal(t, "11111111-2222-3333-4444-555555555555", got.Keyset.ID)
			assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, got.Keyset.Fingerprint)
			// JSON decodes numbers as float64 — Run restores a timestamp column's unix-µs
			// from this.
			require.Len(t, got.Keyset.SortValues, 2)
			assert.InDelta(t, 1_700_000_000_000_000.0, got.Keyset.SortValues[0], 1)
			assert.Equal(t, "indexed", got.Keyset.SortValues[1])
		},
	)

	t.Run("empty token is offset page 1", func(t *testing.T) {
		t.Parallel()
		got, err := listquery.DecodeToken("")
		require.NoError(t, err)
		assert.Nil(t, got.Keyset)
		assert.Equal(t, int32(1), got.OffsetPage)
	})

	t.Run("malformed token is a clean InvalidInput", func(t *testing.T) {
		t.Parallel()
		_, err := listquery.DecodeToken("!!!not-base64!!!")
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})
}
