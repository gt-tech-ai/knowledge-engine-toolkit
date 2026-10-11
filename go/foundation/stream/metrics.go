package stream

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// metricsBuffer decorates a Buffer with read-ahead depth gauges and a producer-wait
// histogram, all labelled with the buffer's name.
type metricsBuffer[T any] struct {
	// inner is the decorated buffer.
	inner Buffer[T]
	// size reports one item's size in bytes.
	size func(T) int
	// items and bytes report the read-ahead held across every stream this buffer wraps.
	items, bytes interfaces.Gauge
	// wait records how long the producer spent between handing an item on and pulling
	// the next, in seconds.
	wait interfaces.Histogram
	// name labels this buffer's series.
	name string
	// heldItems and heldBytes are the totals the gauges report.
	heldItems, heldBytes atomic.Int64
}

// WithMetrics wraps inner so every stream it buffers reports, under the label name, the
// items and bytes read from the source but not yet delivered
// (stream_buffer_items, stream_buffer_bytes) and the producer's wait between items
// (stream_buffer_producer_wait_seconds). size reports one item's size in bytes. The
// metrics are registered here, at construction; apply it at the composition root.
func WithMetrics[T any](
	inner Buffer[T],
	m interfaces.Metrics,
	name string,
	size func(T) int,
) Buffer[T] {
	return &metricsBuffer[T]{
		inner: inner,
		name:  name,
		size:  size,
		items: m.Gauge("stream_buffer_items",
			"Items read from the source but not yet delivered", "buffer"),
		bytes: m.Gauge("stream_buffer_bytes",
			"Bytes read from the source but not yet delivered", "buffer"),
		wait: m.Histogram("stream_buffer_producer_wait_seconds",
			"Producer wait between handing an item on and pulling the next, in seconds",
			[]float64{.001, .005, .01, .05, .1, .5, 1, 5, 10}, "buffer"),
	}
}

// Wrap implements Buffer.
func (b *metricsBuffer[T]) Wrap(
	ctx context.Context,
	src interfaces.Stream[T],
) interfaces.Stream[T] {
	s := &meteredStream[T]{buf: b}
	s.out = b.inner.Wrap(ctx, &meteredSource[T]{Stream: src, s: s})
	return s
}

// add moves the held totals by items and bytes and reports them.
func (b *metricsBuffer[T]) add(items, bytes int64) {
	b.items.Set(float64(b.heldItems.Add(items)), b.name)
	b.bytes.Set(float64(b.heldBytes.Add(bytes)), b.name)
}

// meteredStream is one stream a metricsBuffer wraps: the inner buffer's output, counted
// as it is delivered.
type meteredStream[T any] struct {
	// buf is the decorator holding the shared series.
	buf *metricsBuffer[T]
	// out is the inner buffer's stream.
	out interfaces.Stream[T]
	// items and bytes are this stream's read-ahead, returned to the totals on Close.
	items, bytes atomic.Int64
}

// Next implements interfaces.Stream, releasing a delivered item from the held totals.
func (s *meteredStream[T]) Next(ctx context.Context) (item T, ok bool, err error) {
	item, ok, err = s.out.Next(ctx)
	if ok {
		n := int64(s.buf.size(item))
		s.items.Add(-1)
		s.bytes.Add(-n)
		s.buf.add(-1, -n)
	}
	return item, ok, err
}

// Close implements interfaces.Stream: once the inner stream (and so its producer) is
// closed, the items it read but never delivered leave the held totals.
func (s *meteredStream[T]) Close() error {
	err := s.out.Close()
	s.buf.add(-s.items.Swap(0), -s.bytes.Swap(0))
	return err
}

// meteredSource taps the source the inner buffer reads, counting each item read and
// timing the producer's wait between items.
type meteredSource[T any] struct {
	interfaces.Stream[T]
	// s is the stream whose read-ahead the items count toward.
	s *meteredStream[T]
	// handed is when the producer last received an item; zero before the first. Only
	// the producer's goroutine touches it.
	handed time.Time
}

// Next implements interfaces.Stream.
func (m *meteredSource[T]) Next(ctx context.Context) (item T, ok bool, err error) {
	if !m.handed.IsZero() {
		m.s.buf.wait.Observe(time.Since(m.handed).Seconds(), m.s.buf.name)
	}
	item, ok, err = m.Stream.Next(ctx)
	if ok {
		n := int64(m.s.buf.size(item))
		m.s.items.Add(1)
		m.s.bytes.Add(n)
		m.s.buf.add(1, n)
		m.handed = time.Now()
	}
	return item, ok, err
}
