package unit_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
)

// TestEnvelope_RoundTrip tests that encoding a value and immediately decoding
// it returns the original value with ok=true.
//
// Why this test is important:
//   - The envelope is the serialization boundary between application code and the cache
//     backend; a broken round-trip returns corrupted data without an error signal
//
// What it tests:
//   - Encode then Decode with matching version returns the original struct and ok=true
func TestEnvelope_RoundTrip(t *testing.T) {
	t.Parallel()

	type user struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	original := user{Name: "Alice", Email: "alice@example.com"}
	version := 1

	data, err := cache.Encode(original, version)
	require.NoError(t, err, "Encode must not return error")

	decoded, ok, err := cache.Decode[user](data, version)
	require.NoError(t, err, "Decode must not return error")
	require.True(t, ok, "Decode must return ok=true for matching version")
	assert.Equal(t, original.Name, decoded.Name)
	assert.Equal(t, original.Email, decoded.Email)
}

// TestEnvelope_VersionMismatch tests that decoding with a different version
// than the one used for encoding returns ok=false with a nil error.
//
// Why this test is important:
//   - Version-aware cache invalidation prevents stale-schema data from being
//     served after a code deployment; a version mismatch that still returns ok=true
//     would corrupt callers with old-format data
//
// What it tests:
//   - Decode with version 2 on data encoded with version 1 returns (zero, false, nil)
func TestEnvelope_VersionMismatch(t *testing.T) {
	t.Parallel()

	type payload struct {
		Value int `json:"value"`
	}

	data, err := cache.Encode(payload{Value: 42}, 1)
	require.NoError(t, err, "Encode must not return error")

	val, ok, err := cache.Decode[payload](data, 2)
	require.NoError(t, err, "Decode must not return error on version mismatch")
	assert.False(t, ok, "Decode must return ok=false for version mismatch")
	assert.Equal(t, 0, val.Value, "Decode must return zero value on version mismatch")
}

// TestEnvelope_InvalidJSON tests that decoding corrupt bytes returns ok=false
// with a non-nil error.
//
// Why this test is important:
//   - Cache backends may return truncated or garbage data after network errors;
//     a silent zero-value return would look like a cache hit and corrupt callers
//
// What it tests:
//   - Decode of non-JSON bytes returns (zero, false, non-nil error)
func TestEnvelope_InvalidJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Value string `json:"value"`
	}

	corrupt := []byte("not-valid-json{{{")

	val, ok, err := cache.Decode[payload](corrupt, 1)
	require.Error(t, err, "Decode must return error for corrupt JSON")
	assert.False(t, ok, "Decode must return ok=false for corrupt JSON")
	assert.Empty(t, val.Value, "Decode must return zero value for corrupt JSON")
}

// TestEnvelope_ZeroValue tests that encoding and decoding a zero-value struct
// round-trips correctly.
//
// Why this test is important:
//   - Zero-value structs are common when caching newly-created entities; the
//     envelope must not conflate a legitimate zero-value cache hit with a cache miss
//
// What it tests:
//   - Encode then Decode of a zero-value struct returns (zero, true, nil)
func TestEnvelope_ZeroValue(t *testing.T) {
	t.Parallel()

	type empty struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	var zero empty
	version := 5

	data, err := cache.Encode(zero, version)
	require.NoError(t, err, "Encode must not return error for zero-value struct")

	decoded, ok, err := cache.Decode[empty](data, version)
	require.NoError(t, err)
	require.True(t, ok, "Decode must return ok=true for zero-value struct")
	assert.Empty(t, decoded.Name)
	assert.Equal(t, 0, decoded.Count)
}

// TestEnvelope_CachedAtIsRecent tests that the CachedAt timestamp embedded in
// the envelope is set to approximately the current time.
//
// Why this test is important:
//   - CachedAt drives cache-age diagnostics and TTL decisions; a zero or
//     arbitrary-epoch timestamp would make all cache-age tooling produce wrong results
//
// What it tests:
//   - The encoded CachedAt field falls within [before-2s, after+2s] of the Encode call
func TestEnvelope_CachedAtIsRecent(t *testing.T) {
	t.Parallel()

	before := time.Now()

	data, err := cache.Encode("hello", 1)
	require.NoError(t, err, "Encode must not return error")

	after := time.Now()

	// Unmarshal the raw JSON to inspect the cached_at field directly.
	var raw struct {
		CachedAt time.Time `json:"cached_at"`
	}
	require.NoError(t, json.Unmarshal(data, &raw), "raw envelope must be valid JSON")

	assert.False(t, raw.CachedAt.Before(before.Add(-2*time.Second)),
		"CachedAt %v should not be before encode time %v", raw.CachedAt, before)
	assert.False(t, raw.CachedAt.After(after.Add(2*time.Second)),
		"CachedAt %v should not be after encode returned %v", raw.CachedAt, after)
}
