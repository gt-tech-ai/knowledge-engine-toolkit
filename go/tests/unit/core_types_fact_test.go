package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// goldenFactPath returns the analytics-fact wire fixture shared with the Python suite.
func goldenFactPath() string {
	return filepath.Join(
		textDotDot,
		textDotDot,
		textDotDot,
		"testdata",
		"analytics_fact.golden.json",
	)
}

// goldenFact is the value the golden fixture encodes.
func goldenFact() types.Fact {
	return types.Fact{
		Cube:  textGenaiCalls,
		OrgID: textOrg1,
		TS: time.Date(
			dateInput,
			dateInputFact,
			dateInputGoldenFact,
			goldenFactHour,
			goldenFactMinute,
			goldenFactSecond,
			goldenFactNanosecond,
			time.UTC,
		),
		Dims: map[string]string{
			textLabel:  "a<b>&c\u2028",
			"provider": "aws.bedrock",
			textModel:  "nova-lite",
			"step":     "generate",
			textTeam:   textT1,
		},
		Measures: map[string]float64{
			textTokensIn: textTokensInFact,
			"tokens_out": tokensOut,
			"duration_s": factDurationSeconds,
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
//   - Fact.Wire is byte-identical to testdata/analytics_fact.golden.json
//     (sorted keys, integral measures as integers, UTC RFC 3339 with trimmed fraction,
//     "<", ">", "&" and U+2028 escaped as Python escapes them)
//   - a non-UTC timestamp still renders in UTC
//   - unmarshalling the fixture yields the original Fact
func TestFact_MarshalMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(goldenFactPath())
	require.NoError(t, err)

	got, err := new(goldenFact()).Wire()
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	shifted := goldenFact()

	shifted.TS = shifted.TS.In(time.FixedZone("EDT", -4*3600))

	gotShifted, err := shifted.Wire()
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
//   - Wire writes these bytes
func TestFact_MarshalEscapesLikePython(t *testing.T) {
	t.Parallel()

	fact := types.Fact{
		Cube:           textC,
		OrgID:          textO,
		IdempotencyKey: textK,
		Schema:         types.FactSchemaVersion,
		TS: time.Date(
			dateInput,
			dateInputPython,
			dateInputLikePython,
			0,
			0,
			0,
			0,
			time.UTC,
		),
		Dims: map[string]string{textTeam: "a<b>&c\u2028\u2029\u00e9"},
	}

	got, err := fact.Wire()
	require.NoError(t, err)

	want := `{"cube":"c","dims":{"team":"a\u003cb\u003e\u0026c\u2028\u2029é"},` +
		`"idempotency_key":"k","measures":{},"org_id":"o","schema":1,"ts":"2026-10-09T00:00:` +
		`00Z"}`
	assert.Equal(t, want, string(got))
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

const (
	// dateInputFact is the date input fact the tests use.
	dateInputFact = 10

	// dateInputGoldenFact is the date input golden fact the tests use.
	dateInputGoldenFact = 9

	// goldenFactHour is the date input golden fact13 the tests use.
	goldenFactHour = 12

	// goldenFactMinute is the date input golden fact14 the tests use.
	goldenFactMinute = 34

	// goldenFactSecond is the date input golden fact15 the tests use.
	goldenFactSecond = 56

	// goldenFactNanosecond is the date input golden fact16 the tests use.
	goldenFactNanosecond = 789000000

	// dateInputLikePython is the date input like python the tests use.
	dateInputLikePython = 9

	// dateInputPython is the date input python the tests use.
	dateInputPython = 10

	// factDurationSeconds is the duration s1 the tests use.
	factDurationSeconds = 1.25

	// textTokensInFact is the text tokens in fact the tests use.
	textTokensInFact = 42

	// tokensOut is the tokens out the tests use.
	tokensOut = 7
)
