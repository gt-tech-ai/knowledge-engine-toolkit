package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/dedup"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/leader"
)

// TestDeduplicator_Memory_FirstSeenThenDuplicate tests exactly-once semantics: a key is
// not a duplicate on first sighting and is a duplicate on every repeat, while distinct
// keys are independent.
//
// Why this test is important:
//   - Under at-least-once delivery a redelivered message must be processed once; the
//     deduplicator is the guard, and a first-seen key wrongly reported as a duplicate
//     would drop a real message.
//
// What it tests:
//   - First Seen(k) is false (process it), the repeat is true (skip it), and a different
//     key is independently first-seen.
func TestDeduplicator_Memory_FirstSeenThenDuplicate(t *testing.T) {
	t.Parallel()

	d := dedup.NewMemory(0) // remember forever
	ctx := context.Background()

	dup, err := d.Seen(ctx, "k1")
	require.NoError(t, err)
	require.False(t, dup, "first sighting must not be a duplicate")

	dup, err = d.Seen(ctx, "k1")
	require.NoError(t, err)
	require.True(t, dup, "a repeat sighting must be a duplicate")

	dup, err = d.Seen(ctx, "k2")
	require.NoError(t, err)
	require.False(t, dup, "a distinct key is tracked independently")
}

// TestLeaderElector_AlwaysLeader tests that the single-process elector always reports
// leadership, so a job wrapped by it runs in dev/single-replica.
//
// Why this test is important:
//   - The single-process default must never withhold leadership, or the guarded job would
//     never run on a single-replica deployment.
//
// What it tests:
//   - AlwaysLeader.IsLeader returns true with no error.
func TestLeaderElector_AlwaysLeader(t *testing.T) {
	t.Parallel()

	isLeader, err := leader.AlwaysLeader{}.IsLeader(context.Background())
	require.NoError(t, err)
	require.True(t, isLeader)
}
