package interceptors

import (
	"context"
	"fmt"
	"runtime/debug"

	"connectrpc.com/connect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// recoveryInterceptor recovers from panics in downstream handlers — for BOTH unary and
// server-streaming RPCs — logging the panic + stack and returning a CodeInternal error
// to the caller instead of letting the panic crash the serving goroutine. It is
// server-side only (WrapStreamingClient is a no-op).
type recoveryInterceptor struct {
	// logger records a recovered panic along with its stack trace.
	logger interfaces.Logger
}

// RecoveryInterceptor returns a server-side interceptor that recovers from panics in
// downstream unary and streaming handlers. The panic value and stack trace are logged
// via the provided logger and an Internal error is returned to the caller.
func RecoveryInterceptor(logger interfaces.Logger) connect.Interceptor {
	return recoveryInterceptor{logger: logger}
}

// logPanic logs a recovered panic for procedure and returns the CodeInternal error sent
// to the caller. Shared by the unary and streaming recovery paths.
func (i recoveryInterceptor) logPanic(
	ctx context.Context,
	procedure string,
	r any,
) error {
	i.logger.WithContext(ctx).Error(
		"panic recovered in Connect handler",
		"panic", fmt.Sprintf("%v", r),
		"stack", string(debug.Stack()),
		"procedure", procedure,
	)
	return connect.NewError(connect.CodeInternal, apperr.Internal("internal error"))
}

// WrapUnary recovers a panic in a unary handler into a CodeInternal error.
func (i recoveryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(
		ctx context.Context,
		req connect.AnyRequest,
	) (resp connect.AnyResponse, err error) {
		defer func() {
			if r := recover(); r != nil {
				resp, err = nil, i.logPanic(ctx, req.Spec().Procedure, r)
			}
		}()
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side panic-recovery interceptor.
func (i recoveryInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler recovers a panic in a server-streaming handler into a
// CodeInternal error, so a panic mid-stream returns a clean error instead of crashing
// the serving goroutine.
func (i recoveryInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = i.logPanic(ctx, conn.Spec().Procedure, r)
			}
		}()
		return next(ctx, conn)
	}
}
