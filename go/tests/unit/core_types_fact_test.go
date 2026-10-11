package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// goldenFactPath returns the analytics-fact wire fixture shared with the Python suite.
func goldenFactPath() string {
	return filepath.Join(
		"..",
		"..",
		"..",
		"testdata",
		"analytics_fact.golden.json",
	)
}

// goldenFact is the value the golden fixture encodes.
func goldenFact() types.Fact {
	return types.Fact{
		Cube:  "genai_calls",
		OrgID: "org-1",
		TS:    time.Date(2026, 10, 9, 12, 34, 56, 789_000_000, time.UTC),
		Dims: map[string]string{
			"provider": "aws.bedrock",
			"model":    "nova-lite",
			"step":     "generate",
			"team":     "t-1",
		},
		Measures: map[string]float64{
			"tokens_in":  42,
			"tokens_out": 7,
			"duration_s": 1.25,
		},
		IdempotencyKey: "4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:generate",
		Schema:         types.FactSchemaVersion,
	}
}

// TestFact_MarshalMatchesGoldenFixture tests that the Go Fact encodes to the exact
// bytes of the shared golden fixture and decodes back to the same value.
//
// Why this test is important:
//   - Python producers publish facts the Go analytics consumer decodes; the Python
//     suite asserts the same file, so key order, number and timestamp rendering
//     cannot drift on one side unnoticed
//
// What it tests:
//   - json.Marshal(fact) is byte-identical to testdata/analytics_fact.golden.json
//     (sorted keys, integral measures as integers, UTC RFC 3339 with trimmed fraction)
//   - a non-UTC timestamp still renders in UTC
//   - unmarshalling the fixture yields the original Fact
func TestFact_MarshalMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(goldenFactPath())
	require.NoError(t, err)

	got, err := json.Marshal(goldenFact())
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	shifted := goldenFact()
	shifted.TS = shifted.TS.In(time.FixedZone("EDT", -4*3600))
	gotShifted, err := json.Marshal(shifted)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(gotShifted))

	var decoded types.Fact
	require.NoError(t, json.Unmarshal(want, &decoded))
	assert.True(t, decoded.TS.Equal(goldenFact().TS))
	decoded.TS = goldenFact().TS
	assert.Equal(t, goldenFact(), decoded)
}

// TestFact_MarshalEscapesLikePython tests that HTML-sensitive and line-separator
// characters encode identically in Go and Python.
//
// Why this test is important:
//   - Dims and keys are free-form; one fact must be one byte string whichever
//     language produced it (the Python suite pins the same expected bytes)
//
// What it tests:
//   - "<", ">", "&", U+2028 and U+2029 are written as the escapes \u003c, \u003e,
//     \u0026, \u2028 and \u2029; other non-ASCII stays raw UTF-8; empty
//     measures render as {}
func TestFact_MarshalEscapesLikePython(t *testing.T) {
	t.Parallel()

	fact := types.Fact{
		Cube: "c", OrgID: "o", IdempotencyKey: "k", Schema: types.FactSchemaVersion,
		TS:   time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
		Dims: map[string]string{"team": "a<b>&c\u2028\u2029\u00e9"},
	}

	got, err := json.Marshal(fact)
	require.NoError(t, err)
	assert.JSONEq(t, `{"cube":"c","dims":{"team":"a\u003cb\u003e\u0026c\u2028\u2029é"},`+
		`"idempotency_key":"k","measures":{},"org_id":"o","schema":1,"ts":"2026-10-09T00:00:`+
		`00Z"}`, string(got))
}

// TestFact_UnmarshalRejectsUnknownSchema tests the wire-version check.
//
// Why this test is important:
//   - A fact from a newer producer may carry fields or semantics this consumer
//     does not know; folding it in silently corrupts the aggregates
//
// What it tests:
//   - a fact whose schema is 2, or absent (0), is CodeInvalidInput
func TestFact_UnmarshalRejectsUnknownSchema(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"cube":"c","dims":{},"idempotency_key":"k","measures":{},"org_id":"o","schema":2,"` +
			`ts":"2026-10-09T00:00:00Z"}`,
		`{"cube":"c","dims":{},"idempotency_key":"k","measures":{},"org_id":"o","ts":"2026-1` +
			`0-09T00:00:00Z"}`,
	} {
		var f types.Fact
		assert.Equal(
			t,
			apperr.CodeInvalidInput,
			apperr.Code(json.Unmarshal([]byte(body), &f)),
			body,
		)
	}
}
