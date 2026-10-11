package unit_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
)

// TestParseUUIDCursor tests the shared keyset-cursor id parser.
//
// Why this test is important:
//   - Keyset pagination binds the cursor's id (as the secondary sort key) into the seek
//     predicate; the ParseID callback must turn the cursor's id string back into a
//     uuid.UUID, rejecting a malformed id rather than binding a zero/garbage value that
//     would corrupt the page boundary.
//
// What it tests:
//   - A valid UUID string parses to the equal uuid.UUID (returned as any).
//   - A malformed string returns an error.
func TestParseUUIDCursor(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	got, err := repository.ParseUUIDCursor(id.String())
	require.NoError(t, err)
	assert.Equal(t, id, got)

	_, err = repository.ParseUUIDCursor("not-a-uuid")
	require.Error(t, err)
}
