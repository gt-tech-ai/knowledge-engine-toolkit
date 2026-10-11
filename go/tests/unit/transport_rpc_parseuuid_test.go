package unit_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	apprpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// TestParseUUID tests the shared proto-id-field UUID parser.
//
// Why this test is important:
//   - Every list/get/mutate RPC parses caller-supplied id strings through this one seam;
//     a malformed id must be rejected as CodeInvalidInput at the transport edge (naming
//     the offending field), never silently coerced or reaching a store as a zero UUID.
//
// What it tests:
//   - A valid UUID string parses to the equal uuid.UUID with no error.
//   - A malformed string returns uuid.Nil + a CodeInvalidInput error whose message names
//     the field.
func TestParseUUID(t *testing.T) {
	t.Parallel()

	want := uuid.New()
	got, err := apprpc.ParseUUID(want.String(), "resource_id")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	bad, err := apprpc.ParseUUID("not-a-uuid", "resource_id")
	require.Error(t, err)
	assert.Equal(t, uuid.Nil, bad)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "resource_id")
}
