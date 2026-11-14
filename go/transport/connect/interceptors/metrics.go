package interceptors

import (
	"context"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// metricsInterceptor records RPC metrics (request count + duration) for every RPC shape.
// A stream is measured as one RPC — one counter increment and one duration observation
// for the whole stream, keyed on the procedure. It is server-side: a client's calls are
// metered by the client tier (clients/rpc/connect/interceptors.ClientInterceptors).
type metricsInterceptor struct {
	// requestsTotal counts RPCs, labelled by service, method, and outcome.
	requestsTotal interfaces.Counter
	// requestDuration records RPC latency as a histogram.
	requestDuration interfaces.Histogram
}

// MetricsInterceptor creates a server interceptor that records RPC metrics using the
// interfaces.Metrics abstraction. Records total requests (with service, method,
// and code labels) and request duration (with service and method labels), for unary
// and streaming RPCs alike.
func MetricsInterceptor(metrics interfaces.Metrics) connect.ServerInterceptor {
	recorder := metricsInterceptor{
		requestsTotal: metrics.Counter(
			"connect_rpc_requests_total",
			"Total number of Connect RPC requests",
			"service", "method", "code",
		),
		requestDuration: metrics.Histogram(
			"connect_rpc_duration_seconds",
			"Connect RPC request duration in seconds",
			types.DefaultDurationBuckets(),
			"service", "method",
		),
	}

	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(
			ctx context.Context,
			spec connect.Spec,
			stream connect.ServerStream,
		) error {
			start := time.Now()
			err := next(ctx, spec, stream)
			recorder.record(spec.Procedure, start, err)

			return err
		}
	}
}

// record observes the duration and increments the request counter for one completed RPC
// (unary or stream).
func (i metricsInterceptor) record(procedure string, start time.Time, err error) {
	service, method := parseConnectProcedure(procedure)
	i.requestDuration.Observe(time.Since(start).Seconds(), service, method)

	code := "ok"
	if err != nil {
		code = connect.CodeOf(err).String()
	}

	i.requestsTotal.Inc(service, method, code)
}
