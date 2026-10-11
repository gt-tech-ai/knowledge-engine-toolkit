package interfaces

import "context"

// Stream is a pull-based sequence of items: the consumer asks for one item at a time,
// so a slow consumer slows the producer instead of growing memory. It is the contract
// every large result travels on — a store's pages, a reducer's frames, a gRPC server
// stream — and the substrate in foundation/stream (FromSlice, Chunk, Collect, Buffer)
// composes over it.
//
// A Stream is used by one goroutine at a time; Close may be called from any goroutine
// to abandon it.
type Stream[T any] interface {
	// Next returns the next item. ok is false at the clean end of the stream (item is
	// the zero value and err is nil), and stays false on every later call. A non-nil
	// err ends the stream with a failure; items already returned remain valid.
	Next(ctx context.Context) (item T, ok bool, err error)

	// Close releases the stream's resources and stops any producer behind it. It is
	// idempotent; a Next after Close reports the end of the stream.
	Close() error
}
