package repository

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ParseUUIDCursor converts a keyset cursor's id string to a uuid.UUID for the seek
// predicate to bind against a uuid-keyed secondary sort column. It is the shared ParseID
// callback for uuid-keyed keyset pagination (returning the parsed id as any to satisfy
// the generic cursor config), rejecting a malformed id rather than binding a zero value.
func ParseUUIDCursor(s string) (any, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse uuid cursor")
	}
	return id, nil
}

// CursorPayload holds the data encoded in a cursor token for keyset pagination.
// It is hand-encoded as "RFC3339Nano|id" (see CursorCodec), so the fields carry
// no struct tags.
type CursorPayload struct {
	// CreatedAt is the creation timestamp used as the primary sort key.
	CreatedAt time.Time

	// ID is the entity identifier used as the tiebreaker when timestamps collide.
	ID string
}

// CursorCodec encodes and decodes opaque cursor tokens for keyset pagination.
// A token is the base64url encoding of a hand-formatted "RFC3339Nano|id" payload
// (see Encode) — not JSON, to avoid reflection on the per-page hot path.
// This enables stable, efficient pagination without OFFSET.
type CursorCodec struct{}

// NewCursorCodec creates a new CursorCodec.
func NewCursorCodec() *CursorCodec {
	return &CursorCodec{}
}

// Encode creates an opaque cursor token from a (created_at, id) tuple.
// The timestamp is normalized to UTC before encoding.
func (c *CursorCodec) Encode(createdAt time.Time, id string) string {
	// Hand-encode "RFC3339Nano|id" instead of json.Marshal (reflection) on this
	// per-page hot path. RFC3339Nano contains no '|' and ids are UUIDs, so
	// the first '|' is an unambiguous separator.
	payload := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// Decode extracts the (created_at, id) tuple from an opaque cursor token.
// Returns CodeInvalidInput if the token cannot be decoded.
func (c *CursorCodec) Decode(cursor string) (CursorPayload, error) {
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return CursorPayload{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"invalid cursor encoding",
		)
	}

	ts, id, found := strings.Cut(string(data), "|")
	if !found {
		return CursorPayload{}, apperr.New(
			apperr.CodeInvalidInput,
			"invalid cursor payload: missing separator",
		)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return CursorPayload{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"invalid cursor payload",
		)
	}

	return CursorPayload{CreatedAt: createdAt, ID: id}, nil
}
