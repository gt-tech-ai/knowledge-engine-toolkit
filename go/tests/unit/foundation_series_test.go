package unit_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// The labels the series tests record under.
const (
	// seriesTier is the tier label.
	seriesTier = "client"

	// seriesName is the name label.
	seriesName = "payments"

	// seriesOp is the operation label.
	seriesOp = "Get"

	// seriesOpenGauge is the gauge number of an open breaker.
	seriesOpenGauge = 2

	// seriesSubjects is how many Recorders share the registry in the sharing test.
	seriesSubjects = 2
)

// TestRecorder_NilRecordsNothing tests that a Recorder built without metrics is nil
// and that every method on it does nothing.
//
// Why this test is important:
//   - every primitive and layer holds a possibly-nil Recorder; a method that
//     dereferenced it would turn "no metrics wired" into a panic on the hot path.
//
// What it tests:
//   - NewRecorder(nil, …) returns nil and each method on it returns without a panic.
func TestRecorder_NilRecordsNothing(t *testing.T) {
	t.Parallel()

	recorder := series.NewRecorder(nil, seriesTier, seriesName)
	require.Nil(t, recorder)

	require.NotPanics(t, func() {
		recorder.Retried(seriesOp)
		recorder.RetryExhausted(seriesOp)
		recorder.SetBreakerState(series.BreakerStateOpen)
		recorder.BreakerMoved(series.BreakerStateOpen)
		recorder.Rejected(seriesOp, series.ReasonRateLimited)
		recorder.TimedOut(seriesOp)
		recorder.SetBulkheadLimit(1)
		recorder.SetThrottleRejectionRatio(1)
		recorder.Hedged(series.HedgeOutcomeFired)
		recorder.DeadLettered(series.DeadLetterOutcomeSent)
		recorder.Deduplicated()
	})
}

// TestRecorder_BindsTierAndNameFirst tests that a Recorder registers each series with
// the tier and name labels first and records the method's labels after them.
//
// Why this test is important:
//   - the label order is the series' identity in Prometheus; a method that passed its
//     values in another order than it registered the names would file every event
//     under the wrong label.
//
// What it tests:
//   - Rejected registers resilience_rejections_total with tier, name, operation,
//     reason and increments it with those values in that order.
//   - SetBreakerState sets the gauge to the state's gauge number under tier, name.
func TestRecorder_BindsTierAndNameFirst(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	metrics := mocks.NewMockMetrics(ctrl)
	rejections := mocks.NewMockCounter(ctrl)
	state := mocks.NewMockGauge(ctrl)

	metrics.EXPECT().
		Counter("resilience_rejections_total", gomock.Any(),
			"tier", "name", "operation", "reason").
		Return(rejections)
	metrics.EXPECT().
		Gauge("resilience_breaker_state", gomock.Any(), "tier", "name").
		Return(state)
	metrics.EXPECT().Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(mocks.NewMockCounter(ctrl)).AnyTimes()
	metrics.EXPECT().Gauge(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(mocks.NewMockGauge(ctrl)).AnyTimes()

	rejections.EXPECT().Inc(seriesTier, seriesName, seriesOp, "rate_limited")
	state.EXPECT().Set(float64(seriesOpenGauge), seriesTier, seriesName)

	recorder := series.NewRecorder(metrics, seriesTier, seriesName)

	recorder.Rejected(seriesOp, series.ReasonRateLimited)
	recorder.SetBreakerState(series.BreakerStateOpen)
}

// TestRecorder_SharesSeriesAcrossSubjects tests that two Recorders over one registry
// share each series and keep their subjects apart by label.
//
// Why this test is important:
//   - every decorated component builds its own Recorder over the process registry; a
//     second registration of the same series must reuse the first, not panic at boot.
//
// What it tests:
//   - two Recorders with different names each count one retry, and the registry holds
//     one sample per name under resilience_retries_total.
func TestRecorder_SharesSeriesAcrossSubjects(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	metrics := prom.NewFromRegistry(registry)

	series.NewRecorder(metrics, seriesTier, seriesName).Retried(seriesOp)
	series.NewRecorder(metrics, seriesTier, "ledger").Retried(seriesOp)

	families, err := registry.Gather()
	require.NoError(t, err)

	samples := 0

	for _, family := range families {
		if family.GetName() == "resilience_retries_total" {
			samples += len(family.GetMetric())
		}
	}

	require.Equal(t, seriesSubjects, samples)
}
