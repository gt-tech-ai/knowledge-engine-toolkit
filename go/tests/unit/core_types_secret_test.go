package unit_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestSecret_MasksEverywhereAndRevealsExplicitly tests that an opaque Secret never
// renders its value through any output path and yields the raw value only via Reveal.
//
// Why this test is important:
//   - The credential Source's non-leak guarantee rests entirely on Secret being
//     un-printable: a value that escapes through a %v dump, a log line, or JSON
//     serialization defeats the design. A one-character regression (e.g. String
//     returning the value) must fail here.
//
// What it tests:
//   - String, the fmt verbs (%s/%v/%q/%#v/%+v), MarshalText, and MarshalJSON all
//     return the mask and never the value; Reveal returns the raw value.
func TestSecret_MasksEverywhereAndRevealsExplicitly(t *testing.T) {
	t.Parallel()
	const raw = "super-secret-token-value"
	s := types.NewSecret(raw)

	require.Equal(t, raw, s.Reveal(), "Reveal must return the raw value")
	assert.Equal(t, "***", s.String(), "String must mask")

	for _, verb := range []string{"%s", "%v", "%q", "%#v", "%+v"} {
		out := fmt.Sprintf(verb, s)
		assert.NotContainsf(t, out, raw, "verb %s leaked the value", verb)
		assert.Containsf(t, out, "***", "verb %s did not render the mask", verb)
	}

	text, err := s.MarshalText()
	require.NoError(t, err)
	assert.NotContains(t, string(text), raw, "MarshalText leaked the value")

	blob, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), raw, "MarshalJSON leaked the value")
	assert.Contains(t, string(blob), "***", "MarshalJSON did not render the mask")
}

// TestSecret_ZeroValue tests that a zero Secret reports empty and reveals the empty
// string.
//
// Why this test is important:
//   - Get returns a coded error for a missing credential, so a zero Secret must be
//     unambiguous (IsZero true, Reveal "") — a caller must never mistake an unset value
//     for a real one.
//
// What it tests:
//   - The zero Secret and NewSecret("") both report IsZero and Reveal "".
func TestSecret_ZeroValue(t *testing.T) {
	t.Parallel()
	assert.True(t, types.Secret{}.IsZero(), "zero Secret must be zero")
	assert.True(t, types.NewSecret("").IsZero(), `NewSecret("") must be zero`)
	assert.Empty(t, types.NewSecret("").Reveal())
}

// TestRef_String tests that a credential Ref renders its Env/Class/Field identifiers
// for logs and errors and carries no secret value.
//
// Why this test is important:
//   - Ref appears in not-found errors and diagnostics; a legible, stable form lets an
//     operator see which credential was referenced without exposing any value.
//
// What it tests:
//   - Ref{dev, bootstrap, client_secret} renders "dev/bootstrap/client_secret".
func TestRef_String(t *testing.T) {
	t.Parallel()
	r := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	assert.Equal(t, "dev/bootstrap/client_secret", r.String())
}
