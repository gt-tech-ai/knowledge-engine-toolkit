// Package stub is the zero-infrastructure metrics querier: every instant query is 0
// and every range query an empty series, with no network I/O.
package stub

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Querier answers every query with a zero value and no error.
type Querier struct{}

// Querier satisfies the metrics-query port.
var _ interfaces.MetricsQuerier = Querier{}

// New returns the stub querier.
func New() Querier { return Querier{} }

// Query returns 0.
func (Querier) Query(context.Context, string) (float64, error) { return 0, nil }

// QueryRange returns an empty series.
func (Querier) QueryRange(
	context.Context, string, time.Time, time.Time, time.Duration,
) ([]types.MetricSample, error) {
	return []types.MetricSample{}, nil
}
