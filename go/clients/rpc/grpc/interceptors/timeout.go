package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
)

// TimeoutClientInterceptor returns a client-side interceptor that enforces a
// deadline on the downstream call. It distinguishes a genuine deadline
// (codes.DeadlineExceeded, reporting the elapsed time) from a caller-side
// cancellation (codes.Canceled): a fast-fail on an already-cancelled — or a
// shorter parent — context is NOT relabeled as a full <timeout> wait, which
// would corrupt latency diagnosis. Any other error is returned unchanged.
func TimeoutClientInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err == nil {
			return nil
		}
		return classifyTimeoutErr(ctx, start, err)
	}
}

// TimeoutStreamClientInterceptor returns a client-side stream interceptor
// that bounds only how long stream *creation* (streamer, including any
// retries wrapped inside it per the canonical ordering) may take — NOT the
// stream's data lifetime. A unary-style whole-call deadline would be wrong
// here: a server-streaming RPC (e.g. a long RAG generation) can legitimately
// run far longer than a reasonable connect/open budget, and killing it at
// the open-timeout would cut off a healthy response.
//
// This races streamer against timeout in a background goroutine rather than
// deriving a deadline context to pass into streamer: a context.WithTimeout
// context handed to streamer becomes the resulting stream's context for its
// entire lifetime (gRPC ties a ClientStream to whatever context created it),
// so there would be no way to "stop enforcing" that deadline once the stream
// opens — the long-lived stream would still be killed when the nominal
// open-timeout elapsed. Instead, streamer is given a plain
// context.WithCancel derivative that carries no deadline of its own (so a
// successfully-opened stream behaves exactly like one created with the
// caller's own ctx for the rest of its life) and is canceled only if the
// open itself times out, so an abandoned in-flight open is torn down instead
// of leaking.
func TimeoutStreamClientInterceptor(timeout time.Duration) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		openCtx, abortOpen := context.WithCancel(ctx)
		// abortOpen tears down an abandoned in-flight open (the error and timeout
		// paths). On success, openCtx carries no deadline and becomes the stream's
		// context for its whole life, so it is deliberately NOT canceled — the
		// `opened` guard makes this deferred cleanup a no-op there. Deferring the
		// cancel (rather than calling it inline only on the two abandon paths)
		// keeps `go vet`'s lostcancel analyzer satisfied that cancel runs on every
		// return path, without changing behavior or ordering.
		opened := false
		defer func() {
			if !opened {
				abortOpen()
			}
		}()
		start := time.Now()

		type result struct {
			stream grpc.ClientStream
			err    error
		}
		resultCh := make(chan result, 1)
		go func() {
			stream, err := streamer(openCtx, desc, cc, method, opts...)
			resultCh <- result{stream: stream, err: err}
		}()

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case res := <-resultCh:
			if res.err != nil {
				// Classify before the deferred abortOpen releases openCtx:
				// canceling first would make openCtx.Err() always read back as
				// Canceled, masking the real termination reason. streamer already
				// finished (with an error), so there is nothing left in flight —
				// the deferred abortOpen (opened stays false) is just bookkeeping.
				return nil, classifyTimeoutErr(openCtx, start, res.err)
			}
			// Success: openCtx carries no deadline, so leave it un-canceled — it
			// behaves like ctx for the rest of the stream's life (bounded by ctx's
			// own lifetime, not this interceptor's open-timeout). Mark opened so
			// the deferred cleanup does not cancel it.
			opened = true
			return res.stream, nil
		case <-timer.C:
			// Open took too long: abandon it. The deferred abortOpen (opened stays
			// false) cancels openCtx, which is sufficient for gRPC to tear down
			// whatever the in-flight streamer call was doing — including a stream
			// that opened right at the boundary — without an explicit Close call.
			return nil, status.Errorf(
				codes.DeadlineExceeded,
				"stream open timed out after %s",
				timeout.Round(time.Millisecond),
			)
		}
	}
}

// classifyTimeoutErr relabels err as DeadlineExceeded or Canceled based on
// ctx's termination reason, reporting the actual elapsed time rather than the
// nominal timeout so a shorter parent deadline isn't misreported as a full
// wait. Any other error is returned unchanged.
func classifyTimeoutErr(ctx context.Context, start time.Time, err error) error {
	switch interceptorcore.ClassifyTimeout(ctx) {
	case interceptorcore.TimeoutDeadline:
		return status.Errorf(
			codes.DeadlineExceeded,
			"request timed out after %s",
			time.Since(start).Round(time.Millisecond),
		)
	case interceptorcore.TimeoutCanceled:
		return status.Error(codes.Canceled, "request canceled")
	case interceptorcore.TimeoutNone:
		// The context is still live: the error is the handler's own.
		return err
	default:
		return err
	}
}
