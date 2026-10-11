package interceptors

import (
	"context"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// LoggingServerInterceptor returns a server-side interceptor that logs the
// method, duration, and error (if any) for each RPC.
func LoggingServerInterceptor(logger interfaces.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()

		resp, err := handler(ctx, req)

		duration := time.Since(start)
		if err != nil {
			logger.WithContext(ctx).Error(
				"gRPC request failed",
				"method", info.FullMethod,
				"duration", duration,
				"code", status.Code(err).String(),
				"error", err.Error(),
			)
		} else {
			logger.WithContext(ctx).Info(
				"gRPC request completed",
				"method", info.FullMethod,
				"duration", duration,
				"code", "OK",
			)
		}

		return resp, err
	}
}

// LoggingClientInterceptor returns a client-side interceptor that logs the
// method, duration, and error (if any) for each outgoing RPC.
func LoggingClientInterceptor(logger interfaces.Logger) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		start := time.Now()

		err := invoker(ctx, method, req, reply, cc, opts...)

		duration := time.Since(start)
		if err != nil {
			logger.WithContext(ctx).Error(
				"gRPC client call failed",
				"method", method,
				"duration", duration,
				"code", status.Code(err).String(),
				"error", err.Error(),
			)
		} else {
			logger.WithContext(ctx).Info(
				"gRPC client call completed",
				"method", method,
				"duration", duration,
				"code", "OK",
			)
		}

		return err
	}
}

// LoggingStreamClientInterceptor returns a client-side stream interceptor
// that logs the method, duration, and error (if any) for each outgoing
// stream. The log line is emitted once the stream finishes — observed via a
// terminal RecvMsg, or via a background watch of ctx for a caller that
// cancels the stream and does not drain it (see loggingClientStream.watch).
func LoggingStreamClientInterceptor(
	logger interfaces.Logger,
) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		start := time.Now()

		log := func(err error) {
			duration := time.Since(start)
			if err != nil {
				logger.WithContext(ctx).Error(
					"gRPC client stream failed",
					"method", method,
					"duration", duration,
					"code", status.Code(err).String(),
					"error", err.Error(),
				)
				return
			}
			logger.WithContext(ctx).Info(
				"gRPC client stream completed",
				"method", method,
				"duration", duration,
				"code", "OK",
			)
		}

		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			log(err)
			return nil, err
		}

		wrapped := &loggingClientStream{
			ClientStream: stream,
			log:          log,
			done:         make(chan struct{}),
		}
		go wrapped.watch(ctx)
		return wrapped, nil
	}
}

// loggingClientStream wraps a grpc.ClientStream to log exactly once when the
// stream finishes.
type loggingClientStream struct {
	// ClientStream is the wrapped gRPC client stream.
	grpc.ClientStream
	// log emits the single terminal log line (nil err means clean completion).
	log func(err error)
	// done is closed once the stream has been finalized, coordinating watch and RecvMsg.
	done chan struct{}
	// once guarantees the terminal log is emitted exactly once.
	once sync.Once
}

// RecvMsg implements grpc.ClientStream, logging exactly once when the stream
// terminates (io.EOF on clean completion, or an error).
func (s *loggingClientStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.finish(err)
	}
	return err
}

// watch logs the stream as canceled/failed as soon as ctx ends, without
// waiting for a RecvMsg call that may never come (e.g. a caller that cancels
// the stream and abandons it without draining). ctx is the caller's own call
// context, unwrapped by this interceptor, so it only ends on a genuine
// cancellation or deadline — never as a side effect of the stream completing
// normally, which would otherwise race a healthy stream's own terminal
// RecvMsg. watch exits without effect once finish has already run via
// RecvMsg.
//
// ctx cancellation and a natural terminal RecvMsg can fire at nearly the same
// instant; the outer select would then pick pseudo-randomly and could log a
// healthy, fully-drained stream as canceled/deadline-exceeded. So on
// ctx.Done() watch re-checks s.done before logging: a RecvMsg that has already
// finalized (the authoritative outcome) wins, and finish's sync.Once keeps the
// log line single-emit even if the two truly interleave. Finalization is keyed
// on RecvMsg, which is correct for server-streaming (this package's only use);
// a client-streaming/bidi SendMsg failure would not finalize until ctx ends.
func (s *loggingClientStream) watch(ctx context.Context) {
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

// finish emits the terminal log line exactly once (io.EOF is logged as clean
// completion, any other error as a failure) and closes done.
func (s *loggingClientStream) finish(err error) {
	s.once.Do(func() {
		defer close(s.done)
		if apperr.StdIs(err, io.EOF) {
			s.log(nil)
			return
		}
		s.log(err)
	})
}
