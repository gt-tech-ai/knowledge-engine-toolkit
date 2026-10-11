package interceptors

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultDurationBuckets returns the default histogram bucket boundaries for RPC
// durations (in seconds). Matches Prometheus DefBuckets.
func defaultDurationBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// metricsInterceptor records RPC metrics (request count + duration) for BOTH unary and
// server-streaming RPCs. A stream is measured as one RPC — one counter increment and one
// duration observation for the whole stream, keyed on the procedure.
//
// It is used on both the server and the client interceptor chains (ServerBuilder + the
// ClientBuilder), so WrapStreamingClient is intentionally a no-op: client-streaming
// metrics are a deliberate, currently-untracked scope boundary (only the
// server-streaming handler is covered), NOT because this interceptor is server-side.
type metricsInterceptor struct {
	// requestsTotal counts RPCs, labelled by service, method, and outcome.
	requestsTotal interfaces.Counter
	// requestDuration records RPC latency as a histogram.
	requestDuration interfaces.Histogram
}

// MetricsInterceptor creates an interceptor that records RPC metrics using the
// interfaces.Metrics abstraction. Records total requests (with service, method,
// and code labels) and request duration (with service and method labels), for unary
// and server-streaming RPCs alike.
func MetricsInterceptor(m interfaces.Metrics) connect.Interceptor {
	return metricsInterceptor{
		requestsTotal: m.Counter(
			"connect_rpc_requests_total",
			"Total number of Connect RPC requests",
			"service", "method", "code",
		),
		requestDuration: m.Histogram(
			"connect_rpc_duration_seconds",
			"Connect RPC request duration in seconds",
			defaultDurationBuckets(),
			"service", "method",
		),
	}
}

// record observes the duration and increments the request counter for one completed RPC
// (unary or stream). Shared by both wrap paths.
func (i metricsInterceptor) record(procedure string, start time.Time, err error) {
	service, method := parseConnectProcedure(procedure)
	i.requestDuration.Observe(time.Since(start).Seconds(), service, method)
	code := "ok"
	if err != nil {
		code = connect.CodeOf(err).String()
	}
	i.requestsTotal.Inc(service, method, code)
}

// WrapUnary records metrics for a unary RPC.
func (i metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		i.record(req.Spec().Procedure, start, err)
		return resp, err
	}
}

// WrapStreamingClient is a no-op (see the type doc: client-streaming metrics are out of
// scope).
func (i metricsInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler records one counter increment + one duration observation for a
// whole server-streaming RPC.
func (i metricsInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		i.record(conn.Spec().Procedure, start, err)
		return err
	}
}
