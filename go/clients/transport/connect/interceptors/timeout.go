package interceptors

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TimeoutInterceptor returns a client-side interceptor that enforces a
// deadline on the downstream call. It distinguishes a genuine deadline
// (CodeDeadlineExceeded, reporting the elapsed time) from a caller-side
// cancellation (CodeCanceled): a fast-fail on an already-cancelled — or a
// shorter parent — context is NOT relabeled as a full <timeout> wait, which
// would corrupt latency diagnosis. Any other error is returned unchanged.
func TimeoutInterceptor(timeout time.Duration) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := time.Now()
			resp, err := next(ctx, req)
			if err == nil {
				return resp, nil
			}
			switch interceptorcore.ClassifyTimeout(ctx) {
			case interceptorcore.TimeoutDeadline:
				// Report the actual elapsed time, not the nominal timeout: when a
				// shorter parent deadline is the binding one, the call did not run
				// for the full timeout, so a fixed label would misreport the latency.
				return nil, connect.NewError(
					connect.CodeDeadlineExceeded,
					apperr.New(
						apperr.CodeTimeout,
						fmt.Sprintf(
							"request timed out after %s",
							time.Since(start).Round(time.Millisecond),
						),
					),
				)
			case interceptorcore.TimeoutCanceled:
				return nil, connect.NewError(
					connect.CodeCanceled,
					apperr.Wrap(ctx.Err(), apperr.CodeCanceled, "request canceled"),
				)
			case interceptorcore.TimeoutNone:
				// The context is still live: the error is the handler's own.
				return resp, err
			default:
				return resp, err
			}
		}
	}
}
