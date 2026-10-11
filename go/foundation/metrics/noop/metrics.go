// Package noop provides a no-op metrics implementation that silently discards
// all metric operations. Used when metrics collection is disabled.
package noop

import (
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertions.
var (
	// Metrics must satisfy interfaces.Metrics.
	_ interfaces.Metrics = (*Metrics)(nil)
	// counter must satisfy interfaces.Counter.
	_ interfaces.Counter = (*counter)(nil)
	// histogram must satisfy interfaces.Histogram.
	_ interfaces.Histogram = (*histogram)(nil)
	// gauge must satisfy interfaces.Gauge.
	_ interfaces.Gauge = (*gauge)(nil)
)

// Metrics implements interfaces.Metrics as a silent no-op.
type Metrics struct{}

// New creates a no-op metrics instance.
func New() *Metrics { return &Metrics{} }

// Counter returns a no-op counter; the name, help, and label arguments are ignored.
func (m *Metrics) Counter(
	_, _ string,
	_ ...string,
) interfaces.Counter {
	return &counter{}
}

// Histogram returns a no-op histogram; the name, help, bucket, and label arguments are
// ignored.
func (m *Metrics) Histogram(_, _ string, _ []float64, _ ...string) interfaces.Histogram {
	return &histogram{}
}

// Gauge returns a no-op gauge; the name, help, and label arguments are ignored.
func (m *Metrics) Gauge(_, _ string, _ ...string) interfaces.Gauge { return &gauge{} }

// Handler returns an http.NotFoundHandler, since no metrics are collected to serve.
func (m *Metrics) Handler() http.Handler { return http.NotFoundHandler() }

// counter implements interfaces.Counter as a no-op.
type counter struct{}

// Inc discards the increment.
func (c *counter) Inc(_ ...string) {}

// Add discards the added value.
func (c *counter) Add(_ float64, _ ...string) {}

// histogram implements interfaces.Histogram as a no-op.
type histogram struct{}

// Observe discards the observed value.
func (h *histogram) Observe(_ float64, _ ...string) {}

// gauge implements interfaces.Gauge as a no-op.
type gauge struct{}

// Set discards the gauge value.
func (g *gauge) Set(_ float64, _ ...string) {}

// Inc discards the increment.
func (g *gauge) Inc(_ ...string) {}

// Dec discards the decrement.
func (g *gauge) Dec(_ ...string) {}
