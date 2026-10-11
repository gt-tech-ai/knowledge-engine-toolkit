package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Default internal load-shedding limits — safety-net ceilings for the internal RPC
// surface (service-to-service calls) that no gateway fronts. Sized generously so normal
// service-to-service load is never shed; they only engage under a genuine burst. Tune per
// service as load data warrants (a follow-up may lift these into config).
//
// NOTE: the bulkhead now also gates server-streaming RPCs, and a
// stream holds its concurrency slot for the whole stream lifetime (unlike a short unary
// call). Since one bulkhead instance is shared across all RPCs on a mount, size
// DefaultInternalMaxConcurrent with streaming hold-time in mind; separate stream/unary
// concurrency budgets are a possible follow-up.
const (
	// DefaultInternalMaxConcurrent caps concurrent in-flight internal requests so
	// a burst can't exhaust goroutines or the DB connection pool.
	DefaultInternalMaxConcurrent = 128

	// DefaultInternalRateLimit is the internal request-rate ceiling (requests/sec).
	DefaultInternalRateLimit = 2000

	// DefaultInternalBurst is the token-bucket burst for the internal rate limiter.
	DefaultInternalBurst = 256
)

// bulkheadInterceptor caps concurrently in-flight requests — unary and server-streaming —
// via the bulkhead, shedding with ResourceExhausted when the concurrency limit is reached
// rather than letting unbounded parallelism exhaust goroutines and the backing resource
// pool. Server-side only (WrapStreamingClient is a no-op). A streamed RPC holds its slot
// for the stream's full lifetime (see the const-block note).
type bulkheadInterceptor struct {
	// bh caps the number of concurrently in-flight requests, shedding when full.
	bh interfaces.Bulkhead
}

// BulkheadInterceptor returns a server-side interceptor that caps the number of
// concurrently in-flight requests (unary and streaming) via the bulkhead. Once the
// concurrency limit is reached it sheds load — rejecting with ResourceExhausted — rather
// than letting unbounded parallelism exhaust goroutines and the backing resource pool
// (e.g. the DB connection pool). Requests within the limit run, and the handler's own
// error is preserved.
//
// It mirrors RateLimitInterceptor, which sheds by request rate; this sheds by in-flight
// concurrency — the dimension that protects a fixed-size resource pool.
func BulkheadInterceptor(bh interfaces.Bulkhead) connect.Interceptor {
	return bulkheadInterceptor{bh: bh}
}

// WrapUnary runs a unary handler only while a bulkhead slot is free, else sheds.
func (i bulkheadInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		var resp connect.AnyResponse
		// interceptorcore.Bulkhead runs the handler only if a concurrency slot is free;
		// ran=false means the handler never ran (load shed), distinguishing a shed from
		// the handler's own error without touching the bulkhead's internal full-sentinel.
		ran, shedErr := interceptorcore.Bulkhead(i.bh, func() error {
			var err error
			resp, err = next(ctx, req)
			return err
		})
		if !ran {
			return nil, connect.NewError(connect.CodeResourceExhausted, shedErr)
		}
		return resp, shedErr
	}
}

// WrapStreamingClient is a no-op — this is a server-side load-shedding interceptor.
func (i bulkheadInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler runs a server-streaming handler only while a bulkhead slot is
// free, else sheds with ResourceExhausted. The slot is
// held for the stream's full lifetime.
func (i bulkheadInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ran, shedErr := interceptorcore.Bulkhead(i.bh, func() error {
			return next(ctx, conn)
		})
		if !ran {
			return connect.NewError(connect.CodeResourceExhausted, shedErr)
		}
		return shedErr
	}
}
