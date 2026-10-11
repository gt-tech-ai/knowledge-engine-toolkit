package interfaces

import "net/http"

// Metrics provides application-level metric recording.
//
// Implementations: Prometheus (default) or any metrics backend that supports
// counters, histograms, and gauges.
//
// All foundation and service code depends on this interface, never on a
// concrete metrics library. Swap implementations via the metrics.New factory.
type Metrics interface {
	// Counter returns a counter that can be incremented.
	Counter(name, help string, labels ...string) Counter

	// Histogram returns a histogram for recording distributions.
	Histogram(name, help string, buckets []float64, labels ...string) Histogram

	// Gauge returns a gauge for recording point-in-time values.
	Gauge(name, help string, labels ...string) Gauge

	// Handler returns an HTTP handler that serves the metrics endpoint.
	Handler() http.Handler
}

// Counter is an incrementing metric.
type Counter interface {
	// Inc increments the counter by 1 with the given label values.
	Inc(labelValues ...string)

	// Add increments the counter by the given value.
	Add(value float64, labelValues ...string)
}

// Histogram records value distributions.
type Histogram interface {
	// Observe records a value with the given label values.
	Observe(value float64, labelValues ...string)
}

// Gauge records point-in-time values.
type Gauge interface {
	// Set sets the gauge to the given value.
	Set(value float64, labelValues ...string)

	// Inc increments the gauge by 1.
	Inc(labelValues ...string)

	// Dec decrements the gauge by 1.
	Dec(labelValues ...string)
}
