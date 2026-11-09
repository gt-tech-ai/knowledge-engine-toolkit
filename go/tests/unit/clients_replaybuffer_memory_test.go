package unit_test

import (
	"context"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// appendSeq appends msg-1..msg-n (payload = the id's bytes) under key "k".
func appendSeq(t *testing.T, b interfaces.ReplayBuffer, n int) {
	t.Helper()

	for i := 1; i <= n; i++ {
		id := "msg-" + itoa(i)
		require.NoError(t, b.Append(context.Background(), textK, id, []byte(id)))
	}
}

func itoa(index int) string {
	if index == 0 {
		return text0
	}

	var buf [20]byte

	pos := len(buf)

	for index > 0 {
		pos--

		buf[pos] = byte('0' + index%10)

		index /= initialIndex
	}

	return string(buf[pos:])
}

func ids(msgs []interfaces.BufferedMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}

	return out
}

// TestReplayBufferMemory_ReplayAfter tests the core replay invariant: ReplayAfter returns
// exactly the gapless tail of buffered messages strictly after the given id, and flags a
// gap (complete=false) when the id is not in the retained window.
//
// Why this test is important:
//   - Reconnect replay's whole guarantee is "no dupes, no gaps": the client that last saw
//     msg-j must receive msg-(j+1)... and nothing it already has. Returning the wrong
//     slice would double-deliver or silently drop streamed output; failing to flag an
//     evicted id as a gap would let the client believe it is caught up when it is missing
//     messages.
//
// What it tests:
//   - after a present id → the tail after it (complete=true); after the newest → empty
//     (complete=true); after an unknown/evicted id → empty + complete=false; on an absent
//     key → complete=false.
func TestReplayBufferMemory_ReplayAfter(t *testing.T) {
	t.Parallel()

	buffer := memory.New(newInputAfter, time.Minute)
	appendSeq(t, buffer, appendSeqInput)

	tail, complete, err := buffer.ReplayAfter(context.Background(), textK, "msg-2")
	require.NoError(t, err)
	assert.True(t, complete, "msg-2 is retained → gapless")
	assert.Equal(t, []string{textMsg3, textMsg4, textMsg5}, ids(tail))

	tail, complete, err = buffer.ReplayAfter(context.Background(), textK, textMsg5)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Empty(t, tail, "caught up → nothing to replay")

	_, complete, err = buffer.ReplayAfter(context.Background(), textK, "msg-999")
	require.NoError(t, err)
	assert.False(t, complete, "an unknown id is a gap")

	_, complete, err = buffer.ReplayAfter(context.Background(), textAbsent, textMsg1)
	require.NoError(t, err)
	assert.False(t, complete, "an absent key is a gap")
}

// TestReplayBufferMemory_Prune tests that Prune drops the acked prefix (up to and
// including the acked id), leaving only the tail — the live-ack path that bounds the
// buffer during a connection.
//
// Why this test is important:
//   - 's live AckMessage lets the client confirm receipt up to a message_id so the server
//     can release that prefix; if Prune dropped too much (the tail) the client would lose
//     un-acked messages on a later reconnect, and if it dropped too little the buffer
//     would grow unbounded.
//
// What it tests:
//   - after Prune(k, msg-3), a ReplayAfter(k, msg-3) still returns msg-4,5
//     (complete=true), and the pruned prefix is gone (ReplayAfter(k, msg-1) is now a
//     gap).
func TestReplayBufferMemory_Prune(t *testing.T) {
	t.Parallel()

	buffer := memory.New(newInputPrune, time.Minute)
	appendSeq(t, buffer, appendSeqInput)

	require.NoError(t, buffer.Prune(context.Background(), textK, textMsg3))

	tail, complete, err := buffer.ReplayAfter(context.Background(), textK, textMsg3)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Equal(t, []string{textMsg4, textMsg5}, ids(tail))

	_, complete, err = buffer.ReplayAfter(context.Background(), textK, textMsg1)
	require.NoError(t, err)
	assert.False(t, complete, "the pruned prefix is a gap")
}

// TestReplayBufferMemory_NBoundEvictsOldest tests that the buffer is bounded to max_size,
// evicting the oldest entries — and that a request for an evicted id is correctly
// reported as a gap.
//
// Why this test is important:
//   - The buffer must not grow without bound (a memory-exhaustion vector); it caps at
//     max_size. But eviction creates a gap for a client that fell far behind — that MUST
//     surface as complete=false so the client full-refreshes rather than receiving a tail
//     with a hole at the front.
//
// What it tests:
//   - with max_size=3, appending msg-1..msg-5 retains only msg-3,4,5; ReplayAfter for the
//     evicted msg-1 is a gap; ReplayAfter(msg-3) returns msg-4,5.
func TestReplayBufferMemory_NBoundEvictsOldest(t *testing.T) {
	t.Parallel()

	buffer := memory.New(newInputOldest, time.Minute)
	appendSeq(t, buffer, appendSeqInput)

	_, complete, err := buffer.ReplayAfter(context.Background(), textK, textMsg1)
	require.NoError(t, err)
	assert.False(t, complete, "msg-1 was evicted by the N-bound → gap")

	tail, complete, err := buffer.ReplayAfter(context.Background(), textK, textMsg3)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Equal(t, []string{textMsg4, textMsg5}, ids(tail))
}

// TestReplayBufferMemory_TTLExpires tests that a key's buffer expires after its TTL,
// after which a reconnect sees a gap.
//
// Why this test is important:
//   - The buffer is a short-lived recovery aid, not durable storage; entries must expire
//     so a client that reconnects long after a drop full-refreshes (gap) rather than
//     replaying stale output.
//
// What it tests:
//   - with a tiny TTL, after the window elapses ReplayAfter reports complete=false (the
//     buffer expired).
func TestReplayBufferMemory_TTLExpires(t *testing.T) {
	t.Parallel()

	b := memory.New(newInputTTLExpires, 20*time.Millisecond)
	appendSeq(t, b, appendSeqInputTTLExpires)

	assert.Eventually(t, func() bool {
		_, complete, err := b.ReplayAfter(context.Background(), textK, textMsg1)

		return err == nil && !complete
	}, time.Second, 5*time.Millisecond, "buffer must expire after its TTL")
}

// TestReplayBufferMemory_ReplayAfterProperty property-tests the no-dupes/no-gaps
// invariant over random-length sequences and random resume points.
//
// Why this test is important:
//   - The example-based tests cover fixed sizes; the replay invariant must hold for ANY n
//     and any resume point j — a single off-by-one in the slice bounds would
//     double-deliver or drop a message for some (n, j) an example test happens not to
//     hit. The property pins the invariant universally.
//
// What it tests:
//   - for any 1≤j≤n (n within the bound), ReplayAfter(msg-j) returns exactly
//     msg-(j+1)…msg-n in order, complete=true — no duplicates, no gaps.
func TestReplayBufferMemory_ReplayAfterProperty(t *testing.T) {
	t.Parallel()

	testReplayBufferMemoryReplayAfterPropertyRunsProperty(t)
}

// replayTailMatches reports whether replaying after message j of an n-message buffer
// returns a complete tail holding exactly messages j+1 through n, in order.
func replayTailMatches(t *testing.T, count, index int) bool {
	t.Helper()

	b := memory.New(newInputMatches, time.Minute)
	appendSeq(t, b, count)

	tail, complete, err := b.ReplayAfter(context.Background(), textK, "msg-"+itoa(index))
	if err != nil || !complete {
		return false
	}

	want := make([]string, 0, count-index)
	for i := index + 1; i <= count; i++ {
		want = append(want, "msg-"+itoa(i))
	}

	return slices.Equal(ids(tail), want)
}

// testReplayBufferMemoryReplayAfterPropertyRunsProperty continues
// testReplayBufferMemoryReplayAfterProperty: runs property.
func testReplayBufferMemoryReplayAfterPropertyRunsProperty(t *testing.T) {
	t.Helper()

	property := func(nRaw, jRaw uint8) bool {
		n := int(nRaw%50) + 1 // 1..50
		j := int(jRaw)%n + 1  // 1..n

		return replayTailMatches(t, n, j)
	}
	require.NoError(
		t,
		quick.Check(
			property,
			fixtures.BuildRef(func(row *quick.Config) {
				row.MaxCount = 200
			}),
		),
	)
}

const (
	// appendSeqInput is the append seq input the tests use.
	appendSeqInput = 5

	// appendSeqInputTTLExpires is the append seq input ttlexpires the tests use.
	appendSeqInputTTLExpires = 3

	// initialIndex is the initial index the tests use.
	initialIndex = 10

	// newInputAfter is the new input after the tests use.
	newInputAfter = 100

	// newInputMatches is the new input matches the tests use.
	newInputMatches = 100

	// newInputOldest is the new input oldest the tests use.
	newInputOldest = 3

	// newInputPrune is the new input prune the tests use.
	newInputPrune = 100

	// newInputTTLExpires is the new input ttlexpires the tests use.
	newInputTTLExpires = 100
)

const (
	// textMsg3 is the literal "msg-3".
	textMsg3 = "msg-3"

	// textMsg4 is the literal "msg-4".
	textMsg4 = "msg-4"

	// textMsg5 is the literal "msg-5".
	textMsg5 = "msg-5"
)
