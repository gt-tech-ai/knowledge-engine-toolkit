package interceptors

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultDurationBuckets returns the default histogram bucket boundaries for RPC
// durations (in seconds). Matches Prometheus DefBuckets.
func defaultDurationBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// MetricsServerInterceptor returns a server-side interceptor that records
// request count (with service, method, code labels) and request duration
// (with service, method labels).
func MetricsServerInterceptor(m interfaces.Metrics) grpc.UnaryServerInterceptor {
	return unary(metrics(m))
}

// MetricsStreamServerInterceptor is MetricsServerInterceptor for streaming RPCs: a
// stream is counted once, by its final status, and timed over its whole life.
func MetricsStreamServerInterceptor(m interfaces.Metrics) grpc.StreamServerInterceptor {
	return stream(metrics(m))
}

// metrics counts an RPC by its final status and observes its duration.
func metrics(m interfaces.Metrics) around {
	requestsTotal := m.Counter(
		"grpc_server_requests_total",
		"Total number of gRPC server requests",
		"service", "method", "code",
	)
	requestDuration := m.Histogram(
		"grpc_server_duration_seconds",
		"gRPC server request duration in seconds",
		defaultDurationBuckets(),
		"service", "method",
	)
	return func(
		ctx context.Context, fullMethod string, next func(context.Context) error,
	) error {
		start := time.Now()
		service, method := parseGRPCMethod(fullMethod)
		err := next(ctx)
		requestDuration.Observe(time.Since(start).Seconds(), service, method)
		code := "OK"
		if err != nil {
			code = status.Code(err).String()
		}
		requestsTotal.Inc(service, method, code)
		return err
	}
}

// parseGRPCMethod extracts service and method from a gRPC full method string.
// gRPC full methods have the format "/package.ServiceName/MethodName".
func parseGRPCMethod(fullMethod string) (service, method string) {
	parts := strings.Split(strings.TrimPrefix(fullMethod, "/"), "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", fullMethod
}
