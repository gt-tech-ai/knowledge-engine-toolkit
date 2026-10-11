package stream

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/semaphore"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Buffer decouples a stream's producer from its consumer.
type Buffer[T any] interface {
	// Wrap returns a stream yielding src's items in order, then src's end or error.
	// Closing the returned stream stops any producer Wrap started and closes src; ctx
	// bounds that producer's reads.
	Wrap(ctx context.Context, src interfaces.Stream[T]) interfaces.Stream[T]
}

// Kind selects the Buffer backend.
type Kind int

const (
	// KindDirect passes the source through with no read-ahead and no goroutine (the
	// default).
	KindDirect Kind = iota
	// KindBounded reads ahead on a producer goroutine into a buffer bounded by item
	// count and bytes; a full buffer blocks the producer (backpressure).
	KindBounded
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindDirect:
		return "direct"
	case KindBounded:
		return "bounded"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// ParseKind maps a config kind string ("direct" | "bounded") to its Kind, failing
// loudly on an unknown value. A composition root uses it to select the backend from
// configuration.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "direct":
		return KindDirect, nil
	case "bounded":
		return KindBounded, nil
	default:
		return 0, apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("unknown stream buffer kind: %q", s))
	}
}

// Config selects and bounds a Buffer.
type Config struct {
	// Kind selects the backend; the zero value is KindDirect.
	Kind Kind `yaml:"kind" mapstructure:"kind"`

	// MaxItems caps the items KindBounded holds read-ahead; it must be positive for
	// that kind.
	MaxItems int `yaml:"max_items" mapstructure:"max_items"`

	// MaxBytes caps the summed size of the items KindBounded holds read-ahead; it must
	// be positive for that kind. One item larger than MaxBytes is still admitted, alone.
	MaxBytes int `yaml:"max_bytes" mapstructure:"max_bytes"`
}

// NewFromConfig builds the Buffer cfg selects, without I/O; size reports one item's
// size in bytes for KindBounded's byte bound. It runs once per item on the producer's
// path, so it should be cheap; a negative size counts as zero. An unknown kind, or
// KindBounded with a non-positive bound or a nil size, is CodeInvalidInput.
func NewFromConfig[T any](cfg Config, size func(T) int) (Buffer[T], error) {
	switch cfg.Kind {
	case KindDirect:
		return direct[T]{}, nil
	case KindBounded:
		if cfg.MaxItems <= 0 || cfg.MaxBytes <= 0 || size == nil {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"stream: the bounded buffer needs positive max_items and max_bytes "+
					"and a size func, got max_items=%d max_bytes=%d",
				cfg.MaxItems, cfg.MaxBytes))
		}
		return bounded[T]{maxItems: cfg.MaxItems, maxBytes: cfg.MaxBytes, size: size}, nil
	default:
		return nil, apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("stream: unknown buffer kind %s", cfg.Kind))
	}
}

// direct is KindDirect: it reads the source on the consumer's own Next calls, so a
// Close from another goroutine reaches the source while its Next may be running — the
// Stream contract every source must honour.
type direct[T any] struct{}

// Wrap implements Buffer.
func (direct[T]) Wrap(_ context.Context, src interfaces.Stream[T]) interfaces.Stream[T] {
	return newLatched(src.Next, src.Close)
}

// bounded is KindBounded.
type bounded[T any] struct {
	// size reports one item's size in bytes.
	size func(T) int
	// maxItems is the read-ahead item bound.
	maxItems int
	// maxBytes is the read-ahead byte bound.
	maxBytes int
}

// Wrap implements Buffer: it starts the producer goroutine. Close cancels it, closes
// src (unblocking a Next that does not watch its context), then waits for it to return.
func (b bounded[T]) Wrap(
	ctx context.Context,
	src interfaces.Stream[T],
) interfaces.Stream[T] {
	ctx, cancel := context.WithCancel(ctx)
	q := &queue[T]{
		ch:   make(chan entry[T], b.maxItems),
		sem:  semaphore.NewWeighted(int64(b.maxBytes)),
		done: make(chan struct{}),
	}
	go q.produce(ctx, src, func(item T) int64 {
		return int64(min(max(b.size(item), 0), b.maxBytes))
	})
	return newLatched(q.next, func() error {
		cancel()
		err := src.Close()
		<-q.done
		return err
	})
}

// entry is one slot of a bounded queue: an item and the bytes it holds, or the
// terminal marker carrying the source's end (err nil) or error.
type entry[T any] struct {
	// item is the buffered item; unset on the terminal entry.
	item T
	// err is the source's error on the terminal entry; nil at a clean end.
	err error
	// n is the bytes item holds against the byte bound.
	n int64
	// end marks the terminal entry.
	end bool
}

// queue is the bounded buffer between one producer goroutine and its consumer.
type queue[T any] struct {
	// ch holds the read-ahead entries; its capacity is the item bound.
	ch chan entry[T]
	// sem holds the bytes of the entries in ch; its weight is the byte bound.
	sem *semaphore.Weighted
	// done is closed when the producer returns.
	done chan struct{}
	// exitErr is why the producer stopped without a terminal entry (its context
	// ended); written before done is closed.
	exitErr error
}

// produce pulls src into the queue until it ends or fails, or ctx ends; a full queue
// blocks it on the item or byte bound.
func (q *queue[T]) produce(
	ctx context.Context,
	src interfaces.Stream[T],
	size func(T) int64,
) {
	defer close(q.done)
	for {
		item, ok, err := src.Next(ctx)
		if err != nil || !ok {
			q.send(ctx, entry[T]{end: true, err: err})
			return
		}
		n := size(item)
		if err := q.sem.Acquire(ctx, n); err != nil {
			q.exitErr = canceled(err)
			return
		}
		if !q.send(ctx, entry[T]{item: item, n: n}) {
			q.sem.Release(n)
			return
		}
	}
}

// send enqueues e, reporting false (and recording why) when ctx ends first.
func (q *queue[T]) send(ctx context.Context, e entry[T]) bool {
	select {
	case q.ch <- e:
		return true
	case <-ctx.Done():
		q.exitErr = canceled(ctx.Err())
		return false
	}
}

// next takes the oldest entry, releasing its bytes to the producer.
func (q *queue[T]) next(ctx context.Context) (item T, ok bool, err error) {
	var zero T
	select {
	case e := <-q.ch:
		return q.take(e)
	case <-ctx.Done():
		return zero, false, canceled(ctx.Err())
	case <-q.done:
		select {
		case e := <-q.ch:
			return q.take(e)
		default:
			return zero, false, q.exitErr
		}
	}
}

// take turns a dequeued entry into Next's result.
func (q *queue[T]) take(e entry[T]) (item T, ok bool, err error) {
	if e.end {
		var zero T
		return zero, false, e.err
	}
	q.sem.Release(e.n)
	return e.item, true, nil
}

// canceled codes a context error ending a stream read: a deadline is CodeTimeout, a
// cancellation CodeCanceled.
func canceled(err error) error {
	return apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled),
		"stream: read ended by its context")
}

// latched enforces the Stream contract over a next/close pair: once next reports the
// end or an error, every later Next repeats it without calling next again; Close runs
// close once; a Next after Close, or one that Close overlaps, reports the end.
type latched[T any] struct {
	// next reads the underlying stream.
	next func(context.Context) (T, bool, error)
	// close releases the underlying stream.
	close func() error
	// err is the error next ended with; nil at a clean end.
	err error
	// closeErr is close's result.
	closeErr error
	// once guards close.
	once sync.Once
	// closed is set by Close, possibly from another goroutine.
	closed atomic.Bool
	// ended is set once next reported the end or an error.
	ended bool
}

// newLatched returns a latched stream over next and close.
func newLatched[T any](
	next func(context.Context) (T, bool, error),
	closeFn func() error,
) *latched[T] {
	return &latched[T]{next: next, close: closeFn}
}

// Next implements interfaces.Stream.
func (l *latched[T]) Next(ctx context.Context) (item T, ok bool, err error) {
	var zero T
	if l.ended {
		return zero, false, l.err
	}
	if l.closed.Load() {
		return zero, false, nil
	}
	item, ok, err = l.next(ctx)
	if l.closed.Load() {
		// Close ran during next: whatever next saw (often the cancellation Close
		// caused), the reader abandoned the stream, so report the end.
		return zero, false, nil
	}
	if err != nil || !ok {
		l.ended, l.err = true, err
		return zero, false, err
	}
	return item, true, nil
}

// Close implements interfaces.Stream.
func (l *latched[T]) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.close() })
	return l.closeErr
}
