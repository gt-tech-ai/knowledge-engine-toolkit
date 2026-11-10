package types

// Histogram bucket boundaries for an operation's duration, in seconds.
const (
	// bucket5ms is the 5 millisecond boundary.
	bucket5ms = .005
	// bucket10ms is the 10 millisecond boundary.
	bucket10ms = .01
	// bucket25ms is the 25 millisecond boundary.
	bucket25ms = .025
	// bucket50ms is the 50 millisecond boundary.
	bucket50ms = .05
	// bucket100ms is the 100 millisecond boundary.
	bucket100ms = .1
	// bucket250ms is the 250 millisecond boundary.
	bucket250ms = .25
	// bucket500ms is the 500 millisecond boundary.
	bucket500ms = .5
	// bucket2500ms is the 2.5 second boundary.
	bucket2500ms = 2.5
	// bucket5s is the 5 second boundary.
	bucket5s = 5
	// bucket10s is the 10 second boundary.
	bucket10s = 10
)

// DefaultDurationBuckets returns the histogram bucket boundaries, in seconds, that every
// tier and transport records an operation's duration under.
func DefaultDurationBuckets() []float64 {
	return []float64{
		bucket5ms,
		bucket10ms,
		bucket25ms,
		bucket50ms,
		bucket100ms,
		bucket250ms,
		bucket500ms,
		1,
		bucket2500ms,
		bucket5s,
		bucket10s,
	}
}
