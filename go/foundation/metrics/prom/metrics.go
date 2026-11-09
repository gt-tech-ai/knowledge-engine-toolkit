// Package prom provides a Prometheus-backed metrics implementation.
package prom

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// sizeBucketStart, sizeBucketFactor and sizeBucketCount lay out the exponential size
// buckets.
const sizeBucketStart, sizeBucketFactor, sizeBucketCount = 100, 10, 6

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

// Metrics implements interfaces.Metrics backed by Prometheus.
type Metrics struct {
	// reg is the Prometheus registry that all counters, histograms, and gauges register
	// with.
	reg *prometheus.Registry
}

// Config holds Prometheus-specific metrics settings.
type Config struct{}

// DefaultConfig returns the default Prometheus metrics configuration.
func DefaultConfig() Config { return Config{} }

// New creates an interfaces.Metrics backed by a fresh Prometheus registry
// with standard Go and process collectors.
func New() *Metrics {
	return &Metrics{reg: newRegistry()}
}

// NewFromConfig creates an interfaces.Metrics from a Prometheus Config.
// Forward-compatible: currently delegates to New() since Config has no fields.
func NewFromConfig(_ Config) *Metrics {
	return New()
}

// NewFromRegistry creates an interfaces.Metrics using an existing registry.
func NewFromRegistry(reg *prometheus.Registry) *Metrics {
	return &Metrics{reg: reg}
}

// Counter registers and returns a labelled counter. If a collector with the
// same name is already registered, the existing one is reused rather than
// duplicated; any other registration error panics.
func (m *Metrics) Counter(name, help string, labels ...string) interfaces.Counter {
	counterVec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        name,
		Help:        help,
		Namespace:   "",
		Subsystem:   "",
		Unit:        "",
		ConstLabels: nil,
	}, labels)

	return &counter{inner: registerOrReuse(m.reg, counterVec)}
}

// Histogram registers and returns a labelled histogram with the given buckets.
// If a collector with the same name is already registered, the existing one is
// reused rather than duplicated; any other registration error panics.
func (m *Metrics) Histogram(
	name, help string,
	buckets []float64,
	labels ...string,
) interfaces.Histogram {
	var histogramOpts prometheus.HistogramOpts

	histogramOpts.Name = name
	histogramOpts.Help = help
	histogramOpts.Buckets = buckets

	histogramVec := prometheus.NewHistogramVec(histogramOpts, labels)

	return &histogram{inner: registerOrReuse(m.reg, histogramVec)}
}

// Gauge registers and returns a labelled gauge. If a collector with the same
// name is already registered, the existing one is reused rather than
// duplicated; any other registration error panics.
func (m *Metrics) Gauge(name, help string, labels ...string) interfaces.Gauge {
	gaugeVec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        name,
		Help:        help,
		Namespace:   "",
		Subsystem:   "",
		Unit:        "",
		ConstLabels: nil,
	}, labels)

	return &gauge{inner: registerOrReuse(m.reg, gaugeVec)}
}

// registerOrReuse registers the collector, returning the already registered one of the
// same name and type instead; any other registration error panics.
func registerOrReuse[C prometheus.Collector](reg *prometheus.Registry, c C) C {
	err := reg.Register(c)
	if err == nil {
		return c
	}

	var are prometheus.AlreadyRegisteredError

	if !apperr.As(err, &are) {
		panic(err)
	}

	existing, ok := are.ExistingCollector.(C)
	if !ok {
		panic(err)
	}

	return existing
}

// Handler returns an HTTP handler that exposes the registry in the Prometheus
// exposition format, with OpenMetrics output enabled.
func (m *Metrics) Handler() http.Handler {
	var handlerOpts promhttp.HandlerOpts

	handlerOpts.EnableOpenMetrics = true

	return promhttp.HandlerFor(m.reg, handlerOpts)
}

// Registry returns the underlying Prometheus registry for advanced usage.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.reg
}

// newRegistry creates a Prometheus registry with standard collectors.
func newRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(stdx.Zero[collectors.ProcessCollectorOpts]()),
	)

	return reg
}

// counter implements interfaces.Counter using prometheus.CounterVec.
type counter struct {
	// inner is the underlying Prometheus counter vector this counter delegates to.
	inner *prometheus.CounterVec
}

// Inc increments the counter cell selected by labelValues by 1.
func (c *counter) Inc(labelValues ...string) {
	c.inner.WithLabelValues(labelValues...).Inc()
}

// Add adds value to the counter cell selected by labelValues.
func (c *counter) Add(value float64, labelValues ...string) {
	c.inner.WithLabelValues(labelValues...).Add(value)
}

// histogram implements interfaces.Histogram using prometheus.HistogramVec.
type histogram struct {
	// inner is the underlying Prometheus histogram vector this histogram delegates to.
	inner *prometheus.HistogramVec
}

// Observe records value in the histogram cell selected by labelValues.
func (h *histogram) Observe(value float64, labelValues ...string) {
	h.inner.WithLabelValues(labelValues...).Observe(value)
}

// gauge implements interfaces.Gauge using prometheus.GaugeVec.
type gauge struct {
	// inner is the underlying Prometheus gauge vector this gauge delegates to.
	inner *prometheus.GaugeVec
}

// Set sets the gauge cell selected by labelValues to value.
func (g *gauge) Set(value float64, labelValues ...string) {
	g.inner.WithLabelValues(labelValues...).Set(value)
}

// Inc increments the gauge cell selected by labelValues by 1.
func (g *gauge) Inc(labelValues ...string) {
	g.inner.WithLabelValues(labelValues...).Inc()
}

// Dec decrements the gauge cell selected by labelValues by 1.
func (g *gauge) Dec(labelValues ...string) {
	g.inner.WithLabelValues(labelValues...).Dec()
}

// HTTPServerMetrics holds counters and histograms for HTTP server monitoring.
type HTTPServerMetrics struct {
	// RequestsTotal counts HTTP requests partitioned by method, path, and status code.
	RequestsTotal *prometheus.CounterVec

	// RequestDuration observes request latency in seconds, partitioned by method and path.
	RequestDuration *prometheus.HistogramVec

	// ResponseSize observes response body sizes in bytes, partitioned by method and path.
	ResponseSize *prometheus.HistogramVec
}

// The HTTP server metrics' shared label names.
const (
	// labelMethod is the HTTP request method label.
	labelMethod = "method"
	// labelPath is the request path label.
	labelPath = "path"
)

// NewHTTPServerMetrics creates and registers HTTP server metrics.
func NewHTTPServerMetrics(reg prometheus.Registerer) *HTTPServerMetrics {
	var sdkHistogramOpts prometheus.HistogramOpts

	sdkHistogramOpts.Name = "http_server_response_size_bytes"
	sdkHistogramOpts.Help = "HTTP response size in bytes"
	sdkHistogramOpts.Buckets = prometheus.ExponentialBuckets(
		sizeBucketStart,
		sizeBucketFactor,
		sizeBucketCount,
	)

	var histogramOpts prometheus.HistogramOpts

	histogramOpts.Name = "http_server_request_duration_seconds"
	histogramOpts.Help = "HTTP request duration in seconds"
	histogramOpts.Buckets = prometheus.DefBuckets

	httpServerMetrics := &HTTPServerMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "http_server_requests_total",
			Help:        "Total HTTP requests processed",
			Namespace:   "",
			Subsystem:   "",
			Unit:        "",
			ConstLabels: nil,
		}, []string{labelMethod, labelPath, "status"}),
		RequestDuration: prometheus.NewHistogramVec(
			histogramOpts,
			[]string{labelMethod, labelPath},
		),
		ResponseSize: prometheus.NewHistogramVec(
			sdkHistogramOpts,
			[]string{labelMethod, labelPath},
		),
	}

	reg.MustRegister(
		httpServerMetrics.RequestsTotal,
		httpServerMetrics.RequestDuration,
		httpServerMetrics.ResponseSize,
	)

	return httpServerMetrics
}
