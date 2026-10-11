package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// BulkheadServerInterceptor returns a server-side interceptor that caps the
// number of concurrently in-flight requests via the bulkhead. Once the
// concurrency limit is reached it sheds load — rejecting with ResourceExhausted —
// rather than letting unbounded parallelism exhaust goroutines and the backing
// resource pool (e.g. the DB connection pool). Requests within the limit run, and
// the handler's own error is preserved.
//
// It mirrors RateLimitServerInterceptor, which sheds by request rate; this sheds
// by in-flight concurrency — the dimension that protects a fixed-size pool.
func BulkheadServerInterceptor(bh interfaces.Bulkhead) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		var resp any
		// interceptorcore.Bulkhead runs the handler only if a concurrency slot is
		// free; ran=false means the handler never ran (load shed), distinguishing a
		// shed from the handler's own error without touching the bulkhead's internal
		// full-sentinel.
		ran, shedErr := interceptorcore.Bulkhead(bh, func() error {
			var err error
			resp, err = handler(ctx, req)
			return err
		})
		if !ran {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"concurrency limit reached: %v",
				shedErr,
			)
		}
		return resp, shedErr
	}
}
