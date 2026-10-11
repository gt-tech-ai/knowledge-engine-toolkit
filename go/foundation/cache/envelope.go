// Package cache provides caching utilities for the foundation layer.
package cache

import (
	"encoding/json"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Envelope wraps a cached value with metadata for version-aware caching.
// When the envelope's version doesn't match the expected version, the cached
// entry is treated as a miss — stale-schema entries are silently evicted.
//
// Serialization is JSON, deliberately, not proto/binary: T is an arbitrary type
// parameter (repository entities: Ent models, domain structs), NOT a proto.Message,
// so proto.Marshal cannot encode it. A cache of one *concrete* proto response type
// can use proto encoding; that pattern does not generalize to Envelope[T].
// The companion cursor codec (repos/repository/cursor.go) drops reflection where
// it can (a fixed 2-field shape); the generic payload here cannot, and a
// binary-header-plus-JSON-payload variant would churn the cache wire format on a
// hot read path for a small, version-bump-only gain — so JSON stays.
type Envelope[T any] struct {
	// Data is the cached value payload.
	Data T `json:"data"`

	// CachedAt records when the envelope was written, for observability and TTL reasoning.
	CachedAt time.Time `json:"cached_at"`

	// Version is the schema version the value was encoded with; a mismatch on
	// Decode is treated as a miss so stale-schema entries self-evict.
	Version int `json:"version"`
}

// Encode serializes value into a versioned envelope as JSON bytes.
func Encode[T any](value T, version int) ([]byte, error) {
	env := Envelope[T]{
		Data:     value,
		CachedAt: time.Now(),
		Version:  version,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "encode cache envelope")
	}
	return b, nil
}

// Decode deserializes bytes into a versioned envelope. Returns (value, true, nil)
// on success, (zero, false, nil) on version mismatch (stale-schema miss), and
// (zero, false, err) on corrupt/invalid JSON.
func Decode[T any](data []byte, version int) (val T, ok bool, err error) {
	var env Envelope[T]
	if err := json.Unmarshal(data, &env); err != nil {
		var zero T
		return zero, false, apperr.Wrap(err, apperr.CodeInternal, "decode cache envelope")
	}
	if env.Version != version {
		var zero T
		return zero, false, nil
	}
	return env.Data, true, nil
}
