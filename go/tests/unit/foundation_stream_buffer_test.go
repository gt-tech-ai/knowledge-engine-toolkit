package unit_test

import (
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stream"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// streamSize is the byte size the buffer tests charge per item: its string length.
func streamSize(s string) int { return len(s) }

// endlessStream returns a mock Stream that yields "ab" forever, counting each pull in
// pulled; it may be closed any number of times.
func endlessStream(t *testing.T, pulled *atomic.Int64) *mocks.MockStream[string] {
	t.Helper()
	src := mocks.NewMockStream[string](gomock.NewController(t))
	src.EXPECT().Next(gomock.Any()).DoAndReturn(func(any) (string, bool, error) {
		pulled.Add(1)
		return "ab", true, nil
	}).AnyTimes()
	src.EXPECT().Close().Return(nil).AnyTimes()
	return src
}

// TestBuffer_BoundedBlocksProducerAtCapacity tests the bounded buffer's backpressure.
//
// Why this test is important:
//   - The bounded buffer is what keeps a fast store from outrunning a slow reader: if
//     the producer kept pulling past the bound, a large result would grow memory
//     without limit instead of slowing the source down.
//
// What it tests:
//   - With no reader, the producer stops after filling the buffer (by item count, or
//     by bytes when that bound is tighter) plus the one item it holds while blocked.
//   - Each item the reader takes lets the producer pull exactly one more.
func TestBuffer_BoundedBlocksProducerAtCapacity(t *testing.T) {
	t.Parallel()
	cases := map[string]stream.Config{
		"item bound": {Kind: stream.KindBounded, MaxItems: 2, MaxBytes: 1 << 20},
		"byte bound": {Kind: stream.KindBounded, MaxItems: 10, MaxBytes: 4},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var pulled atomic.Int64
			buf, err := stream.NewFromConfig(cfg, streamSize)
			require.NoError(t, err)
			out := buf.Wrap(t.Context(), endlessStream(t, &pulled))
			defer func() { require.NoError(t, out.Close()) }()

			require.Eventually(t, func() bool { return pulled.Load() == 3 },
				time.Second, time.Millisecond)
			require.Never(t, func() bool { return pulled.Load() > 3 },
				50*time.Millisecond, time.Millisecond)

			item, ok, err := out.Next(t.Context())
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "ab", item)
			require.Eventually(t, func() bool { return pulled.Load() == 4 },
				time.Second, time.Millisecond)
			require.Never(t, func() bool { return pulled.Load() > 4 },
				50*time.Millisecond, time.Millisecond)
		})
	}
}

// TestBuffer_DeliversItemsBeforeSourceError tests error ordering through each kind.
//
// Why this test is important:
//   - A source that fails after yielding rows must not lose them: the reader sees every
//     row read before the failure, then the failure, so a partial answer is never
//     silently presented as complete.
//
// What it tests:
//   - Both kinds yield the source's items in order, then its error, which every later
//     Next repeats; Close closes the source exactly once.
func TestBuffer_DeliversItemsBeforeSourceError(t *testing.T) {
	t.Parallel()
	for _, kind := range []stream.Kind{stream.KindDirect, stream.KindBounded} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			boom := apperr.New(apperr.CodeUnavailable, "store down")
			buf, err := stream.NewFromConfig(
				stream.Config{Kind: kind, MaxItems: 1, MaxBytes: 1}, streamSize)
			require.NoError(t, err)
			out := buf.Wrap(t.Context(), scriptedStream(t, []string{"aa", "bb"}, boom))

			var got []string
			for {
				item, ok, err := out.Next(t.Context())
				if err != nil {
					require.ErrorIs(t, err, boom)
					break
				}
				require.True(t, ok)
				got = append(got, item)
			}
			require.Equal(t, []string{"aa", "bb"}, got)
			_, ok, err := out.Next(t.Context())
			require.False(t, ok)
			require.ErrorIs(t, err, boom)
			require.NoError(t, out.Close())
			require.NoError(t, out.Close())
		})
	}
}

// TestBuffer_CloseStopsProducer tests that closing a bounded stream ends its producer.
//
// Why this test is important:
//   - A reader that abandons a result (a closed socket, a cancelled request) must not
//     leave a goroutine blocked on a full buffer, holding the source open forever.
//
// What it tests:
//   - After Close no buffer goroutine remains (goleak), the source is closed, and Next
//     reports the end of the stream.
func TestBuffer_CloseStopsProducer(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	var pulled atomic.Int64
	src := mocks.NewMockStream[string](gomock.NewController(t))
	src.EXPECT().Next(gomock.Any()).DoAndReturn(func(any) (string, bool, error) {
		pulled.Add(1)
		return "ab", true, nil
	}).AnyTimes()
	src.EXPECT().Close().Return(nil).Times(1)

	buf, err := stream.NewFromConfig(
		stream.Config{
			Kind:     stream.KindBounded,
			MaxItems: 1,
			MaxBytes: 1 << 10,
		},
		streamSize,
	)
	require.NoError(t, err)
	out := buf.Wrap(t.Context(), src)
	_, ok, err := out.Next(t.Context())
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, out.Close())
	_, ok, err = out.Next(t.Context())
	require.NoError(t, err)
	require.False(t, ok)
}

// TestBuffer_MetricsRecordDepthAndBlockedTime tests the buffer metrics decorator.
//
// Why this test is important:
//   - A buffer pinned at its bound with a long producer wait is the signal that a
//     reader is the bottleneck of a large result; without the gauges and the wait
//     histogram that saturation is invisible until memory or latency alarms fire.
//
// What it tests:
//   - With no reader, the item and byte gauges report the read-ahead held (the two
//     queued items plus the one the blocked producer holds); a read lowers them by one
//     item; Close returns both to zero.
//   - The producer's wait between handing an item on and pulling the next is observed
//     on the wait histogram, labelled with the buffer's name.
func TestBuffer_MetricsRecordDepthAndBlockedTime(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockMetrics(ctrl)
	itemsGauge, bytesGauge := mocks.NewMockGauge(ctrl), mocks.NewMockGauge(ctrl)
	wait := mocks.NewMockHistogram(ctrl)
	var items, bytes atomic.Int64
	var waited atomic.Bool
	m.EXPECT().Gauge("stream_buffer_items", gomock.Any(), "buffer").Return(itemsGauge)
	m.EXPECT().Gauge("stream_buffer_bytes", gomock.Any(), "buffer").Return(bytesGauge)
	m.EXPECT().
		Histogram("stream_buffer_producer_wait_seconds", gomock.Any(), gomock.Any(),
			"buffer").
		Return(wait)
	itemsGauge.EXPECT().Set(gomock.Any(), "frames").Do(func(v float64, _ ...string) {
		items.Store(int64(v))
	}).AnyTimes()
	bytesGauge.EXPECT().Set(gomock.Any(), "frames").Do(func(v float64, _ ...string) {
		bytes.Store(int64(v))
	}).AnyTimes()
	wait.EXPECT().Observe(gomock.Any(), "frames").Do(func(v float64, _ ...string) {
		if v > 0 {
			waited.Store(true)
		}
	}).AnyTimes()

	inner, err := stream.NewFromConfig(
		stream.Config{
			Kind:     stream.KindBounded,
			MaxItems: 2,
			MaxBytes: 1 << 10,
		},
		streamSize,
	)
	require.NoError(t, err)
	buf := stream.WithMetrics(inner, m, "frames", streamSize)
	var pulled atomic.Int64
	out := buf.Wrap(t.Context(), endlessStream(t, &pulled))

	require.Eventually(t, func() bool { return items.Load() == 3 && bytes.Load() == 6 },
		time.Second, time.Millisecond)
	_, ok, err := out.Next(t.Context())
	require.NoError(t, err)
	require.True(t, ok)
	require.Eventually(t, waited.Load, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return items.Load() == 3 && pulled.Load() == 4 },
		time.Second, time.Millisecond)

	require.NoError(t, out.Close())
	require.Equal(t, int64(0), items.Load())
	require.Equal(t, int64(0), bytes.Load())
}

// TestBuffer_UnknownKindFailsLoudly tests the buffer factory's validation.
//
// Why this test is important:
//   - A mistyped kind or a zero bound in configuration must stop the process at boot,
//     not silently run unbuffered or with a buffer that can never hold an item.
//
// What it tests:
//   - ParseKind maps "direct" and "bounded" and rejects anything else with
//     CodeInvalidInput; NewFromConfig rejects an unknown Kind and a bounded config with
//     a non-positive bound; the direct kind passes items through unchanged.
func TestBuffer_UnknownKindFailsLoudly(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]stream.Kind{
		"direct": stream.KindDirect, "bounded": stream.KindBounded,
	} {
		got, err := stream.ParseKind(s)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, s, got.String())
	}
	_, err := stream.ParseKind("ring")
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	for name, cfg := range map[string]stream.Config{
		"unknown kind":   {Kind: stream.Kind(math.MaxInt8)},
		"zero items":     {Kind: stream.KindBounded, MaxItems: 0, MaxBytes: 1},
		"negative bytes": {Kind: stream.KindBounded, MaxItems: 1, MaxBytes: -1},
	} {
		_, err := stream.NewFromConfig(cfg, streamSize)
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	buf, err := stream.NewFromConfig(stream.Config{}, streamSize)
	require.NoError(t, err)
	got, err := stream.Collect(t.Context(),
		buf.Wrap(t.Context(), stream.FromSlice([]string{"a", "b"})), streamSize, 8)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got)
}
