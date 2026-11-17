package unit_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Observations for TestDefaultDurationBuckets_ResolveSlowOperations, in seconds.
const (
	// slowParseSeconds is a parse slower than 30 s but under a minute.
	slowParseSeconds = 45
	// slowRoundSeconds is an index round slower than 2 min but under 5.
	slowRoundSeconds = 250
)

// TestDefaultDurationBuckets_ResolveSlowOperations tests that the shared duration
// buckets tell slow operations apart up to five minutes.
//
// Why this test is important:
//   - Ingestion parses, Bedrock calls and KB rounds run for tens of seconds to
//     minutes; with a 10 s top bucket every one of them lands in +Inf, so a p95
//     over them reads as "above 10 s" and a regression from 40 s to 4 min is
//     invisible.
//
// What it tests:
//   - A 45 s observation falls in the 60 s bucket and not the 30 s one.
//   - A 250 s observation falls in the 300 s bucket and not the 120 s one.
//   - The boundaries strictly increase, as Prometheus requires.
func TestDefaultDurationBuckets_ResolveSlowOperations(t *testing.T) {
	t.Parallel()

	buckets := types.DefaultDurationBuckets()
	for i := 1; i < len(buckets); i++ {
		require.Greater(t, buckets[i], buckets[i-1], "bucket %d", i)
	}

	histogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:                       "",
		Subsystem:                       "",
		Name:                            "op_duration_seconds",
		Help:                            "operation duration",
		Unit:                            "",
		ConstLabels:                     nil,
		Buckets:                         buckets,
		NativeHistogramBucketFactor:     0,
		NativeHistogramZeroThreshold:    0,
		NativeHistogramMaxBucketNumber:  0,
		NativeHistogramMinResetDuration: 0,
		NativeHistogramMaxZeroThreshold: 0,
		NativeHistogramMaxExemplars:     0,
		NativeHistogramExemplarTTL:      0,
	})
	histogram.Observe(slowParseSeconds)
	histogram.Observe(slowRoundSeconds)

	var metric dto.Metric

	require.NoError(t, histogram.Write(&metric))

	counts := map[float64]uint64{}
	for _, bucket := range metric.GetHistogram().GetBucket() {
		counts[bucket.GetUpperBound()] = bucket.GetCumulativeCount()
	}

	assert.Equal(t,
		[]uint64{0, 1, 1, 2},
		[]uint64{counts[30], counts[60], counts[120], counts[300]},
		"cumulative counts at le=30, 60, 120, 300")
}
