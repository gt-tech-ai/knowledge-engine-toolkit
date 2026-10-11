package interfaces

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// MetricsQuerier evaluates metric queries (PromQL) against a metrics backend. A backend
// that measures fails closed: a value it can't measure is an error, never a silent zero.
// The zero-infrastructure stub backend is the deliberate exception: it measures nothing
// and answers zero and an empty series so the graph boots without a metrics server.
type MetricsQuerier interface {
	// Query evaluates query as an instant query and returns its single sample value.
	Query(ctx context.Context, query string) (float64, error)

	// QueryRange evaluates query over [start, end] at step and returns its single
	// series; no series is an empty series.
	QueryRange(
		ctx context.Context,
		query string,
		start, end time.Time,
		step time.Duration,
	) ([]types.MetricSample, error)
}
