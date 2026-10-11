// Package stream is the engine's substrate for large, pull-based results over
// interfaces.Stream.
//
// FromSlice adapts an in-memory slice. Chunk splits items into ordered groups no
// larger than a byte budget, so one oversized result travels as several bounded
// frames. Collect drains a stream into a slice under a byte budget and fails with
// CodeResourceExhausted, closing the source, once the budget is spent — the guard a
// request/response surface puts in front of an unbounded stream. Buffer decouples a
// producer from its consumer: the bounded Kind reads ahead into a buffer capped by
// item count and bytes, blocking the producer when it is full (backpressure), and the
// direct Kind passes the stream through unchanged. NewFromConfig selects the Kind, and
// WithMetrics decorates a Buffer with read-ahead depth gauges and a producer-wait
// histogram.
package stream
