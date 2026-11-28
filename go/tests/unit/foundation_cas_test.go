package unit_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

const (
	// casVectorHash is `printf 'd\x00{"a":1}' | shasum -a 256`, computed outside Go.
	casVectorHash = "0569dca7aceb3c33d7d38c7f938a716e07cda454b75e3906b0f8b7bfb02827fb"
	// casPropertyRuns is how many random documents the stability property checks.
	casPropertyRuns = 500
)

// casJSON renders v as JSON text, or "null" when v has no JSON form.
func casJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "null"
	}

	return string(raw)
}

// casScrambled writes doc as JSON with each object's keys rotated by shift positions
// and whitespace between tokens: the same value as json.Marshal, other bytes.
func casScrambled(doc map[string]map[string]float64, shift uint8) string {
	pads := []string{"", " ", "\n\t"}
	pad := pads[int(shift)%len(pads)]

	return casObject(casKeys(doc, shift), pad, func(outer string) string {
		inner := doc[outer]

		return casObject(casKeys(inner, shift), pad, func(key string) string {
			return casJSON(inner[key])
		})
	})
}

// casKeys returns the keys of m sorted, then rotated left by shift.
func casKeys[V any](m map[string]V, shift uint8) []string {
	keys := slices.Sorted(maps.Keys(m))
	if len(keys) == 0 {
		return keys
	}

	at := int(shift) % len(keys)

	return append(keys[at:], keys[:at]...)
}

// casObject writes one JSON object with the given key order, padding every token and
// taking each value's text from value.
func casObject(keys []string, pad string, value func(key string) string) string {
	parts := make([]string, 0, len(keys))

	for _, key := range keys {
		parts = append(parts, pad+casJSON(key)+pad+":"+pad+value(key))
	}

	return "{" + strings.Join(parts, ",") + pad + "}"
}

// TestFoundationCAS_HashIgnoresKeyOrderAndWhitespace tests cas.Canonical and cas.Sum.
//
// Why this test is important:
//   - A record is stored under the hash of what produced it; a hash that moved with key
//     order or spacing would store one value twice and make a lookup miss
//
// What it tests:
//   - two spellings of one object (reordered keys, extra whitespace) hash equal; the
//     same bytes under chartspec/v1 and nlp-annotation/v1 hash apart; Sum matches the
//     SHA-256 of "d", NUL and the canonical bytes computed outside Go; HashOf checks its
//     domain and Canonical refuses invalid JSON, both CodeInvalidInput; and a
//     500-document property check finds the hash stable under key shuffling and
//     whitespace.
func TestFoundationCAS_HashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	t.Parallel()

	const domain = types.Domain("d")

	one, err := cas.Canonical([]byte(`{"b": 2,  "a": 1}`))
	require.NoError(t, err)

	two, err := cas.Canonical([]byte("{\n\"a\":1,\"b\":2}"))
	require.NoError(t, err)

	assert.JSONEq(t, `{"a":1,"b":2}`, string(one))
	assert.Equal(t, cas.Sum(domain, one), cas.Sum(domain, two))
	assert.NotEqual(t,
		cas.Sum("chartspec/v1", one),
		cas.Sum("nlp-annotation/v1", one),
	)

	canonical, err := cas.Canonical([]byte(`{ "a" : 1 }`))
	require.NoError(t, err)

	vector := cas.Sum(domain, canonical)
	assert.Equal(t, casVectorHash, vector.String())

	fromValue, err := cas.HashOf(domain, map[string]int{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, casVectorHash, fromValue.String())

	_, err = cas.HashOf("", map[string]int{"a": 1})
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, err = cas.Canonical([]byte(`{"a":`))
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, err = cas.CanonicalOf(make(chan int))
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	stable := func(doc map[string]map[string]float64, shift uint8) bool {
		compact, mErr := json.Marshal(doc)
		if mErr != nil {
			return false
		}

		wantCanonical, cErr := cas.Canonical(compact)
		gotCanonical, gErr := cas.Canonical([]byte(casScrambled(doc, shift)))

		return cErr == nil && gErr == nil &&
			cas.Sum(domain, wantCanonical) == cas.Sum(domain, gotCanonical)
	}

	require.NoError(t, quick.Check(stable, fixtures.BuildRef(func(row *quick.Config) {
		row.MaxCount = casPropertyRuns
	})))
}
