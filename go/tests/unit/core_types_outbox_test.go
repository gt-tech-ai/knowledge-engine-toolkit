package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestOutboxPartition_IsFnv32aModPartitions tests the one partition hash.
//
// Why this test is important:
//   - Every writer (the engine store and a product's own INSERT) must put a key in
//     the same partition, and a relay drains only its partition, so a hash that
//     drifted would strand rows no relay claims
//
// What it tests:
//   - pinned FNV-1a 32-bit vectors: "" mod 4 = 1, "a" mod 8 = 4, "doc-1" mod 4 = 3,
//     "doc-2" mod 8 = 6; fewer than two partitions always give 0
func TestOutboxPartition_IsFnv32aModPartitions(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, types.OutboxPartition("", partitions4))
	assert.Equal(t, wantOutboxPartition4, types.OutboxPartition(textA, partitions8))
	assert.Equal(t, wantOutboxPartition3, types.OutboxPartition(textDoc1, partitions4))
	assert.Equal(t, wantOutboxPartition6, types.OutboxPartition("doc-2", partitions8))
	assert.Equal(t, 0, types.OutboxPartition(textDoc1, 1))
	assert.Equal(t, 0, types.OutboxPartition(textDoc1, 0))
}
