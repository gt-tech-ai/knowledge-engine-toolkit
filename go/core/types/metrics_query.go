package types

import "time"

// MetricSample is one point of a metrics range-query series.
type MetricSample struct {
	// Time is the sample's evaluation timestamp.
	Time time.Time

	// Value is the sample's finite value.
	Value float64
}
