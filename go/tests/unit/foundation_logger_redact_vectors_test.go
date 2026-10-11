package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger"
)

// redactVector is one shared PII-redaction case: an input and its exact redacted form.
type redactVector struct {
	// Name labels the case in test output.
	Name string `json:"name"`
	// Input is the raw text handed to the redactor.
	Input string `json:"input"`
	// Want is the exact redacted output.
	Want string `json:"want"`
}

// TestRedactPII_SharedVectors tests that RedactPII produces the exact output of every
// case in the repo-level testdata/redact_vectors.json.
//
// Why this test is important:
//   - The Python redact_pii is a port of RedactPII and reads the same file; pinning
//     both languages to one vector set keeps a log line (or a span event) redacted
//     identically whichever language emitted it
//
// What it tests:
//   - For each vector, RedactPII(input) equals want exactly
func TestRedactPII_SharedVectors(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(
		filepath.Join("..", "..", "..", "testdata", "redact_vectors.json"),
	)
	require.NoError(t, err)
	var vectors []redactVector
	require.NoError(t, json.Unmarshal(raw, &vectors))
	require.NotEmpty(t, vectors)

	for _, v := range vectors {
		assert.Equal(t, v.Want, logger.RedactPII(v.Input), v.Name)
	}
}
