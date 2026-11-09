package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestMetricsBuilder_KindPrometheus tests that the metrics factory produces a
// working Prometheus implementation.
//
// Why this test is important:
//   - Prometheus metrics drive all production alerting and dashboards; a broken
//     factory means services start without any observability
//   - Validates the factory-to-Prometheus wiring that all services depend on
//
// What it tests:
//   - metrics.New with KindPrometheus returns a non-nil Metrics without error
//   - Counter creation and Inc do not panic
func TestMetricsBuilder_KindPrometheus(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err, "metrics.New(KindPrometheus) must not return error")
	require.NotNil(t, m, textExpectedNonNilMetrics)

	// Verify basic metrics operations work through the interface
	counter := m.Counter("test_counter", "test counter help")
	counter.Inc()
}

// TestMetricsBuilder_DisabledReturnsNoop tests that disabled metrics return a
// no-op implementation that silently accepts all operations.
//
// Why this test is important:
//   - In test and local-dev environments, metrics are often disabled; the no-op
//     must accept all calls without panicking or services crash at startup
//   - Callers should not need nil-checks around metric operations
//
// What it tests:
//   - Config with Enabled=false returns a non-nil Metrics
//   - Counter Inc, Add, Histogram Observe, and Gauge Set/Inc/Dec do not panic
func TestMetricsBuilder_DisabledReturnsNoop(t *testing.T) {
	t.Parallel()

	cfg := metrics.Config{
		Kind:    metrics.KindPrometheus,
		Enabled: false,
		Path:    "",
	}

	currentMetrics, err := metrics.NewFromConfig(cfg)
	require.NoError(t, err, "NewFromConfig with Enabled=false must not return error")
	require.NotNil(t, currentMetrics, "expected non-nil metrics even when disabled")

	// Verify no-op metrics operations don't panic
	counter := currentMetrics.Counter("test_noop_counter", "test counter", textMethod)
	counter.Inc(textGetUpper)
	counter.Add(addInputNoop, textPost)

	hist := currentMetrics.Histogram("test_noop_hist", "test histogram", nil, textMethod)
	hist.Observe(observeInputNoop, textGetUpper)

	gauge := currentMetrics.Gauge("test_noop_gauge", "test gauge")
	gauge.Set(gaugeReadingNoop)
	gauge.Inc()
	gauge.Dec()
}

// TestMetricsBuilder_UnknownKindReturnsError tests that the metrics factory
// rejects unsupported backends.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil metrics instance that panics on the first Counter/Histogram call
//
// What it tests:
//   - metrics.New with an unknown Kind value returns a non-nil error
func TestMetricsBuilder_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := metrics.New(metrics.Kind(kindInputError))
	require.Error(t, err, textExpectedErrorForUnknown)
}

// TestMetricsBuilder_NewFromConfig tests that NewFromConfig creates metrics
// from an explicit config struct.
//
// Why this test is important:
//   - NewFromConfig is the primary constructor used in Wire-injected services;
//     it must work with fully specified config structs from YAML/env
//
// What it tests:
//   - NewFromConfig with Enabled=true and KindPrometheus returns a non-nil Metrics
//     without error
func TestMetricsBuilder_NewFromConfig(t *testing.T) {
	t.Parallel()

	cfg := metrics.Config{
		Kind:    metrics.KindPrometheus,
		Enabled: true,
		Path:    "",
	}

	m, err := metrics.NewFromConfig(cfg)
	require.NoError(t, err, "NewFromConfig must not return error")
	require.NotNil(t, m, textExpectedNonNilMetrics)
}

// TestMetricsBuilder_DefaultConfig tests that the default metrics config
// provides production-ready settings.
//
// Why this test is important:
//   - Services that omit explicit metrics config inherit these defaults;
//     disabled-by-default would silently suppress all production metrics
//   - Serves as a living specification for default observability behavior
//
// What it tests:
//   - Default kind is KindPrometheus
//   - Metrics are enabled by default
func TestMetricsBuilder_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := metrics.DefaultConfig()
	assert.Equal(
		t,
		metrics.KindPrometheus,
		cfg.Kind,
		"default kind should be KindPrometheus",
	)
	assert.True(t, cfg.Enabled, "metrics must be enabled by default")
}

// TestMetricsBuilder_WithOptions tests that the factory accepts functional
// options to customize configuration.
//
// Why this test is important:
//   - Custom metrics paths are needed when running alongside other services
//     that expose metrics on the default path; broken options silently ignore
//     the customization
//
// What it tests:
//   - metrics.New with WithPath option returns a non-nil Metrics without error
func TestMetricsBuilder_WithOptions(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus, metrics.WithPath("/custom-metrics"))
	require.NoError(t, err, "metrics.New with WithPath must not return error")
	require.NotNil(t, m, textExpectedNonNilMetrics)
}

// TestMetricsBuilder_KindString tests the string representation of metrics Kind
// values, covering all switch branches.
//
// Why this test is important:
//   - Kind.String() appears in error messages and logs
//
// What it tests:
//   - KindPrometheus -> "prometheus"
//   - Unknown Kind -> "Kind(N)" format
func TestMetricsBuilder_KindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		kind metrics.Kind
	}{
		{textPrometheus, metrics.KindPrometheus},
		{textKind99, metrics.Kind(kindInput)},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.kind.String(), "Kind(%d).String()", tt.kind)
	}
}

// TestMetricsBuilder_ConfigToOptions tests that Config.ToOptions produces a
// functional option slice that reproduces the original config.
//
// Why this test is important:
//   - ToOptions is used for config round-tripping; if the resulting options
//     don't reproduce the original config, services start with wrong settings
//
// What it tests:
//   - DefaultConfig.ToOptions applied to a blank config reproduces the original
func TestMetricsBuilder_ConfigToOptions(t *testing.T) {
	t.Parallel()

	original := metrics.DefaultConfig()
	opts := original.ToOptions()

	var rebuilt metrics.Config

	for _, opt := range opts {
		opt(&rebuilt)
	}

	assert.Equal(t, original.Kind, rebuilt.Kind)
	assert.Equal(t, original.Enabled, rebuilt.Enabled)
	assert.Equal(t, original.Path, rebuilt.Path)
}

// TestMetricsBuilder_WithEnabled tests that the WithEnabled option correctly
// sets the Enabled flag.
//
// Why this test is important:
//   - WithEnabled(false) is the mechanism for disabling metrics in test and
//     local-dev environments; if it doesn't work, metrics are never disabled
//
// What it tests:
//   - New with WithEnabled(false) returns a noop metrics (no error)
func TestMetricsBuilder_WithEnabled(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus, metrics.WithEnabled(false))
	require.NoError(t, err, "metrics.New with WithEnabled(false) must not return error")
	require.NotNil(t, m, "expected non-nil metrics even when disabled")

	// Should return a noop that doesn't panic.
	c := m.Counter("disabled_counter", textTest)
	c.Inc()
}

// TestMetricsBuilder_PromHistogramBuckets tests that Prometheus histograms
// accept custom bucket boundaries.
//
// Why this test is important:
//   - Different operations need different bucket boundaries (e.g., cache vs.
//     database latency); incorrect bucket handling would produce unusable
//     histograms
//
// What it tests:
//   - Histogram with custom buckets does not panic
//   - Observe calls with labels succeed
func TestMetricsBuilder_PromHistogramBuckets(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err, textMetricsNewMustNot)

	h := m.Histogram("prom_test_histogram_buckets", "test histogram",
		[]float64{0.001, 0.01, 0.1, 0.5, 1.0, 5.0}, textMethod)

	h.Observe(observeInput, textGetUpper)
	h.Observe(observeInputBuckets, textPost)
}

// TestMetricsBuilder_PromCounterAdd tests that Prometheus Counter.Add with
// labels records values correctly.
//
// Why this test is important:
//   - Counter.Add is used for batch metric increments; it must accept float
//     values and label combinations without panicking
//
// What it tests:
//   - Counter.Add with float values and label values does not panic
func TestMetricsBuilder_PromCounterAdd(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err, textMetricsNewMustNot)

	c := m.Counter("prom_test_counter_add", "test counter", textMethod, textStatus)
	c.Inc(textGetUpper, text200)
	c.Add(addInput, textPost, "201")
	c.Add(addInputAdd, textDeleteUpper, "204")
}

// TestMetricsBuilder_PromGaugeIncDec tests that Prometheus Gauge Inc/Dec
// operations work with labels.
//
// Why this test is important:
//   - Gauge.Inc/Dec track active resource counts (connections, goroutines);
//     broken operations would produce incorrect resource monitoring
//
// What it tests:
//   - Gauge.Set, Inc, and Dec with labels do not panic
func TestMetricsBuilder_PromGaugeIncDec(t *testing.T) {
	t.Parallel()

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err, textMetricsNewMustNot)

	g := m.Gauge("prom_test_gauge_incdec", "test gauge", textService)
	g.Set(gaugeReading, textAPI)
	g.Inc(textAPI)
	g.Dec(textAPI)
}

// TestMetricsBuilder_PromAlreadyRegistered tests that re-registering a counter
// with the same name returns the existing collector rather than panicking.
//
// Why this test is important:
//   - Hot-reloading or multiple init calls may attempt duplicate registration;
//     the implementation must handle AlreadyRegisteredError gracefully
//
// What it tests:
//   - Creating two counters, histograms, and gauges with the same name does
//     not panic and returns valid metric instances
func TestMetricsBuilder_PromAlreadyRegistered(t *testing.T) {
	t.Parallel()

	currentMetrics, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err, textMetricsNewMustNot)

	// Counter: register twice with same name and identical descriptor.
	c1 := currentMetrics.Counter("prom_dup_counter", "duplicate counter", textMethod)
	secondC := currentMetrics.Counter("prom_dup_counter", "duplicate counter", textMethod)

	c1.Inc(textGetUpper)
	secondC.Inc(textPost)

	// Histogram: register twice with same name and identical descriptor.
	histogram := currentMetrics.Histogram(
		"prom_dup_histogram",
		"duplicate histogram",
		nil,
		textMethod,
	)
	currentHistogram := currentMetrics.Histogram(
		"prom_dup_histogram",
		"duplicate histogram",
		nil,
		textMethod,
	)

	histogram.Observe(observeInputRegistered, textGetUpper)
	currentHistogram.Observe(observeInputAlreadyRegistered, textPost)

	// Gauge: register twice with same name and identical descriptor.
	g1 := currentMetrics.Gauge("prom_dup_gauge", "duplicate gauge", textService)
	secondG := currentMetrics.Gauge("prom_dup_gauge", "duplicate gauge", textService)

	g1.Set(gaugeReadingRegistered, textAPI)
	secondG.Set(gaugeReadingAlreadyRegistered, textAPI)
}

// TestMetrics_MockAsConsumerDependency tests that MockMetrics satisfies
// interfaces.Metrics and can be used by consumers that depend on the interface.
//
// Why this test is important:
//   - Service-layer unit tests mock the metrics interface; the mock must
//     implement Counter, Histogram, and Gauge or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockMetrics returns MockCounter, MockHistogram, and MockGauge
//   - Counter.Inc, Histogram.Observe, and Gauge.Set delegate to mocks
func TestMetrics_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockMetrics := mocks.NewMockMetrics(ctrl)
	mockCounter := mocks.NewMockCounter(ctrl)
	mockHist := mocks.NewMockHistogram(ctrl)
	mockGauge := mocks.NewMockGauge(ctrl)

	mockMetrics.EXPECT().
		Counter("http_requests_total", "Total HTTP requests", textMethod).
		Return(mockCounter)
	mockMetrics.EXPECT().
		Histogram("request_duration_seconds", "Request duration", gomock.Any(), textMethod).
		Return(mockHist)
	mockMetrics.EXPECT().
		Gauge("active_connections", "Active connections").
		Return(mockGauge)

	mockCounter.EXPECT().Inc(textGetUpper)
	mockHist.EXPECT().Observe(observeInputDependency, textGetUpper)
	mockGauge.EXPECT().Set(gaugeReadingDependency)

	var currentMetrics interfaces.Metrics = mockMetrics

	c := currentMetrics.Counter("http_requests_total", "Total HTTP requests", textMethod)
	c.Inc(textGetUpper)

	histogram := currentMetrics.Histogram(
		"request_duration_seconds",
		"Request duration",
		nil,
		textMethod,
	)
	histogram.Observe(observeInputDependency, textGetUpper)

	g := currentMetrics.Gauge("active_connections", "Active connections")
	g.Set(gaugeReadingDependency)
}

const (
	// addInputAdd is the add input add the tests use.
	addInputAdd = 0.5

	// addInputNoop is the add input noop the tests use.
	addInputNoop = 5

	// gaugeReadingAlreadyRegistered is the gauge reading already registered the tests use.
	gaugeReadingAlreadyRegistered = 2.0

	// gaugeReadingDependency is the gauge reading dependency the tests use.
	gaugeReadingDependency = 42.0

	// gaugeReadingNoop is the gauge reading noop the tests use.
	gaugeReadingNoop = 42.0

	// gaugeReadingRegistered is the gauge reading registered the tests use.
	gaugeReadingRegistered = 1.0

	// observeInputAlreadyRegistered is the observe input already registered the tests use.
	observeInputAlreadyRegistered = 0.2

	// observeInputBuckets is the observe input buckets the tests use.
	observeInputBuckets = 2.5

	// observeInputDependency is the observe input dependency the tests use.
	observeInputDependency = 0.5

	// observeInputNoop is the observe input noop the tests use.
	observeInputNoop = 0.5

	// observeInputRegistered is the observe input registered the tests use.
	observeInputRegistered = 0.1
)

const (
	// textMetricsNewMustNot is the literal "metrics.New must not return error".
	textMetricsNewMustNot = "metrics.New must not return error"
)
