package unit_test

import (
	"slices"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stream"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestStream_FromSliceYieldsInOrderThenEnds tests the slice-backed stream.
//
// Why this test is important:
//   - FromSlice is the stream every test and every in-memory producer starts from; a
//     reordered, dropped or repeated item would corrupt whatever reads it, and a stream
//     that kept yielding past its end would hang a consumer.
//
// What it tests:
//   - The items come back in order, then Next reports the clean end (ok false, no
//     error) and keeps reporting it; Close is idempotent.
func TestStream_FromSliceYieldsInOrderThenEnds(t *testing.T) {
	t.Parallel()
	s := stream.FromSlice([]string{"a", "b", "c"})
	var got []string
	for {
		item, ok, err := s.Next(t.Context())
		require.NoError(t, err)
		if !ok {
			break
		}
		got = append(got, item)
	}
	require.Equal(t, []string{"a", "b", "c"}, got)
	item, ok, err := s.Next(t.Context())
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, item)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
}

// TestChunk_SplitsAtMaxBytesKeepingOrder tests the byte-bounded chunker as a property.
//
// Why this test is important:
//   - Chunk is how one oversized result travels as several bounded frames; a chunk over
//     the budget breaks the transport limit it exists to respect, and a dropped or
//     reordered item corrupts the result the receiver reassembles.
//
// What it tests:
//   - For random item sizes and budgets, the chunks concatenate back to the input in
//     order, every chunk is non-empty and fits the budget, and no two neighbouring
//     chunks could have been one (the split is greedy).
func TestChunk_SplitsAtMaxBytesKeepingOrder(t *testing.T) {
	t.Parallel()
	law := func(raw []uint8, budget uint8) bool {
		maxBytes := int(budget)%64 + 1
		items := make([]int, len(raw))
		for i, r := range raw {
			items[i] = int(r)%maxBytes + 1
		}
		chunks, err := stream.Chunk(items, func(n int) int { return n }, maxBytes)
		if err != nil {
			return false
		}
		var flat []int
		prev := -1
		for _, c := range chunks {
			total := 0
			for _, n := range c {
				total += n
			}
			if len(c) == 0 || total > maxBytes || (prev >= 0 && prev+c[0] <= maxBytes) {
				return false
			}
			prev = total
			flat = append(flat, c...)
		}
		return slices.Equal(flat, items)
	}
	require.NoError(t, quick.Check(law, &quick.Config{MaxCount: 300}))
}

// TestChunk_OversizedItemIsResourceExhausted tests the chunker's failure modes.
//
// Why this test is important:
//   - An item that alone exceeds the budget can never be sent; failing loudly with a
//     retry-later code tells the caller to take a different path instead of emitting a
//     frame the transport rejects. A non-positive budget is a configuration bug.
//
// What it tests:
//   - An item larger than maxBytes is CodeResourceExhausted; maxBytes <= 0 is
//     CodeInvalidInput; no input yields no chunks.
func TestChunk_OversizedItemIsResourceExhausted(t *testing.T) {
	t.Parallel()
	size := func(s string) int { return len(s) }

	_, err := stream.Chunk([]string{"ab", "toolong"}, size, 4)
	require.Equal(t, apperr.CodeResourceExhausted, apperr.Code(err))

	_, err = stream.Chunk([]string{"a"}, size, 0)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	chunks, err := stream.Chunk(nil, size, 4)
	require.NoError(t, err)
	require.Empty(t, chunks)
}

// TestCollect_StopsAtBudgetAndClosesSource tests the bounded collector.
//
// Why this test is important:
//   - Collect is the guard a request/response surface puts in front of an unbounded
//     stream: without it one large result is buffered whole in memory. Stopping must
//     also close the source, or its producer (a store cursor, a gRPC stream) leaks.
//
// What it tests:
//   - Within the budget every item comes back in order and the source is closed.
//   - Past the budget Collect returns CodeResourceExhausted without reading further and
//     closes the source.
//   - A source error is returned as-is and the source is closed.
//   - A non-positive budget is CodeInvalidInput, and the source is still closed.
func TestCollect_StopsAtBudgetAndClosesSource(t *testing.T) {
	t.Parallel()
	size := func(s string) int { return len(s) }

	t.Run("within budget", func(t *testing.T) {
		t.Parallel()
		src := scriptedStream(t, []string{"aa", "bb"}, nil)
		got, err := stream.Collect(t.Context(), src, size, 4)
		require.NoError(t, err)
		require.Equal(t, []string{"aa", "bb"}, got)
	})

	t.Run("over budget", func(t *testing.T) {
		t.Parallel()
		// Exactly three reads are expected: a fourth fails the test as unexpected.
		src := mocks.NewMockStream[string](gomock.NewController(t))
		gomock.InOrder(
			src.EXPECT().Next(gomock.Any()).Return("aa", true, nil),
			src.EXPECT().Next(gomock.Any()).Return("bb", true, nil),
			src.EXPECT().Next(gomock.Any()).Return("cc", true, nil),
		)
		src.EXPECT().Close().Return(nil).Times(1)
		_, err := stream.Collect(t.Context(), src, size, 5)
		require.Equal(t, apperr.CodeResourceExhausted, apperr.Code(err))
	})

	t.Run("non-positive budget", func(t *testing.T) {
		t.Parallel()
		src := scriptedStream(t, nil, nil)
		_, err := stream.Collect(t.Context(), src, size, 0)
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})

	t.Run("source error", func(t *testing.T) {
		t.Parallel()
		boom := apperr.New(apperr.CodeUnavailable, "store down")
		src := scriptedStream(t, []string{"aa"}, boom)
		_, err := stream.Collect(t.Context(), src, size, 8)
		require.ErrorIs(t, err, boom)
	})
}

// scriptedStream returns a mock Stream that yields items, then ends with endErr (the
// clean end when nil), and must be closed exactly once. A Next past the scripted
// items fails the test through gomock's unexpected-call check.
func scriptedStream(
	t *testing.T,
	items []string,
	endErr error,
) *mocks.MockStream[string] {
	t.Helper()
	src := mocks.NewMockStream[string](gomock.NewController(t))
	calls := make([]any, 0, len(items)+1)
	for _, item := range items {
		calls = append(calls, src.EXPECT().Next(gomock.Any()).Return(item, true, nil))
	}
	calls = append(
		calls,
		src.EXPECT().Next(gomock.Any()).Return("", false, endErr).MaxTimes(1),
	)
	gomock.InOrder(calls...)
	src.EXPECT().Close().Return(nil).Times(1)
	return src
}
