package listquery

import (
	"encoding/base64"
	"encoding/json"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The opaque page token: a URL-safe base64 of a compact JSON encoding of the
// core/types.PageToken union. It is backend-internal — clients treat it as a blob (the
// numeric pager synthesizes offset tokens, infinite scroll echoes the server's keyset
// token) — so it needs no protobuf and no cross-language schema; stdlib json + base64
// suffice, keeping the codec pure (core/types only) and out of the repos/ORM tier
// entirely.

// tokenWire is the JSON shape of a page token. Short keys keep the base64 compact; the
// union is a present offset OR a present keyset, never both.
type tokenWire struct {
	// Keyset is the keyset cursor, present when the token is a keyset page.
	Keyset *keysetWire `json:"k,omitempty"`
	// OffsetPage is the 1-based page number, present when the token is an offset page.
	OffsetPage int32 `json:"o,omitempty"`
}

// keysetWire is the JSON shape of a keyset cursor. Fingerprint is a []byte, which json
// encodes as a base64 string; SortValues are scalars (string/number/bool) — a timestamp
// rides as a unix-µs number.
type keysetWire struct {
	// SortValues are the cursor's sort-key values as JSON-encodable scalars (a timestamp
	// as unix-µs).
	SortValues []any `json:"s"`
	// ID is the cursor's tiebreaker id value.
	ID string `json:"i"`
	// Fingerprint identifies the sort spec the cursor was minted for; json-encoded as
	// base64.
	Fingerprint []byte `json:"f"`
}

// EncodeToken serializes a PageToken to the opaque URL-safe base64 token. A keyset
// cursor's SortValues must already be JSON-encodable scalars (a timestamp as a unix-µs
// int64); the caller (Run) converts a time.Time before minting, so this never sees an
// unencodable value.
func EncodeToken(tok types.PageToken) (string, error) {
	var w tokenWire
	if tok.Keyset != nil {
		w.Keyset = &keysetWire{
			SortValues:  tok.Keyset.SortValues,
			ID:          tok.Keyset.ID,
			Fingerprint: tok.Keyset.Fingerprint,
		}
	} else {
		w.OffsetPage = tok.OffsetPage
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "marshal page token")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeToken parses an opaque page token into its semantic PageToken. An empty token
// defaults to offset page 1; a malformed/undecodable token is an InvalidInput (never a
// panic), so a client carrying a stale/garbage cursor gets a clean reset-to-page-1 signal
// rather than a 500. A keyset cursor's SortValues come back as their JSON kinds (a number
// as float64) — Run restores a timestamp column's unix-µs number to time.Time before
// compiling the seek.
func DecodeToken(token string) (types.PageToken, error) {
	if token == "" {
		return types.PageToken{OffsetPage: 1}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return types.PageToken{}, apperr.InvalidInput("malformed page token")
	}
	var w tokenWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return types.PageToken{}, apperr.InvalidInput("malformed page token")
	}
	if w.Keyset != nil {
		return types.PageToken{Keyset: &types.KeysetCursor{
			SortValues:  w.Keyset.SortValues,
			ID:          w.Keyset.ID,
			Fingerprint: w.Keyset.Fingerprint,
		}}, nil
	}
	return types.PageToken{OffsetPage: w.OffsetPage}, nil
}
