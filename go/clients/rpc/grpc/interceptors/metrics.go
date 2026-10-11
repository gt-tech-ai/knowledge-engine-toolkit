package interceptors

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		service, method := parseGRPCMethod(info.FullMethod)

		resp, err := handler(ctx, req)

		duration := time.Since(start).Seconds()
		requestDuration.Observe(duration, service, method)

		code := "OK"
		if err != nil {
			code = status.Code(err).String()
		}
		requestsTotal.Inc(service, method, code)

		return resp, err
	}
}

// MetricsClientInterceptor returns a client-side interceptor that records
// request count (with service, method, code labels) and request duration
// (with service, method labels).
func MetricsClientInterceptor(m interfaces.Metrics) grpc.UnaryClientInterceptor {
	requestsTotal := m.Counter(
		"grpc_client_requests_total",
		"Total number of gRPC client requests",
		"service", "method", "code",
	)
	requestDuration := m.Histogram(
		"grpc_client_duration_seconds",
		"gRPC client request duration in seconds",
		defaultDurationBuckets(),
		"service", "method",
	)

	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		start := time.Now()
		service, rpcMethod := parseGRPCMethod(method)

		err := invoker(ctx, method, req, reply, cc, opts...)

		duration := time.Since(start).Seconds()
		requestDuration.Observe(duration, service, rpcMethod)

		code := "OK"
		if err != nil {
			code = status.Code(err).String()
		}
		requestsTotal.Inc(service, rpcMethod, code)

		return err
	}
}

// MetricsStreamClientInterceptor returns a client-side stream interceptor
// that records stream count (with service, method, code labels) and stream
// duration (with service, method labels). Duration spans the full stream
// lifetime, from creation through the point the stream finishes — observed
// via a terminal RecvMsg, or via a background watch of ctx for a caller that
// cancels the stream and does not drain it (see metricsClientStream.watch).
func MetricsStreamClientInterceptor(m interfaces.Metrics) grpc.StreamClientInterceptor {
	requestsTotal := m.Counter(
		"grpc_client_stream_requests_total",
		"Total number of gRPC client streams",
		"service", "method", "code",
	)
	requestDuration := m.Histogram(
		"grpc_client_stream_duration_seconds",
		"gRPC client stream duration in seconds",
		defaultDurationBuckets(),
		"service", "method",
	)

	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		start := time.Now()
		service, rpcMethod := parseGRPCMethod(method)

		record := func(err error) {
			requestDuration.Observe(time.Since(start).Seconds(), service, rpcMethod)
			code := "OK"
			if err != nil {
				code = status.Code(err).String()
			}
			requestsTotal.Inc(service, rpcMethod, code)
		}

		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			record(err)
			return nil, err
		}

		wrapped := &metricsClientStream{
			ClientStream: stream,
			record:       record,
			done:         make(chan struct{}),
		}
		go wrapped.watch(ctx)
		return wrapped, nil
	}
}

// metricsClientStream wraps a grpc.ClientStream to record count/duration
// metrics exactly once when the stream finishes, since
// MetricsStreamClientInterceptor's streamer call returns before the stream
// actually completes.
type metricsClientStream struct {
	// ClientStream is the wrapped gRPC client stream.
	grpc.ClientStream
	// record emits the single count/duration metric (nil err means clean completion).
	record func(err error)
	// done is closed once the stream has been finalized, coordinating watch and RecvMsg.
	done chan struct{}
	// once guarantees the terminal metric is recorded exactly once.
	once sync.Once
}

// RecvMsg implements grpc.ClientStream, recording metrics exactly once when
// the stream terminates (io.EOF on clean completion, or an error).
func (s *metricsClientStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.finish(err)
	}
	return err
}

// watch records the stream as canceled/failed as soon as ctx ends, without
// waiting for a RecvMsg call that may never come (e.g. a caller that cancels
// the stream and abandons it without draining). ctx is the caller's own call
// context, unwrapped by this interceptor, so it only ends on a genuine
// cancellation or deadline — never as a side effect of the stream completing
// normally, which would otherwise race a healthy stream's own terminal
// RecvMsg. watch exits without effect once finish has already run via
// RecvMsg.
//
// ctx cancellation and a natural terminal RecvMsg can fire at nearly the same
// instant; the outer select would then pick pseudo-randomly and could record
// a healthy, fully-drained stream as canceled/deadline-exceeded. So on
// ctx.Done() watch re-checks s.done before recording: a RecvMsg that has
// already finalized (the authoritative outcome) wins, and finish's sync.Once
// keeps the record single-emit even if the two truly interleave. Finalization
// is keyed on RecvMsg, which is correct for server-streaming (this package's
// only use); a client-streaming/bidi SendMsg failure would not finalize until
// ctx ends.
func (s *metricsClientStream) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		select {
		case <-s.done: // a terminal RecvMsg already won — defer to its outcome
		default:
			s.finish(status.FromContextError(ctx.Err()).Err())
		}
	case <-s.done:
	}
}

// finish records the terminal count/duration metric exactly once (io.EOF is
// treated as clean completion, any other error as a failure) and closes done.
func (s *metricsClientStream) finish(err error) {
	s.once.Do(func() {
		defer close(s.done)
		if apperr.StdIs(err, io.EOF) {
			s.record(nil)
			return
		}
		s.record(err)
	})
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
