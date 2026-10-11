package interceptors

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errctx"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// is4xxConnectCode reports whether the Connect error code maps to an HTTP 4xx
// status. 4xx codes represent normal client conditions and must not trip an
// alert that fires on Error-level logs.
func is4xxConnectCode(code connect.Code) bool {
	switch code {
	case connect.CodeInvalidArgument,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodeUnauthenticated,
		connect.CodePermissionDenied,
		connect.CodeFailedPrecondition,
		connect.CodeOutOfRange,
		connect.CodeResourceExhausted,
		// A caller that cancels/disconnects mid-request (499 client-closed-request) is a client
		// outcome, not a server fault — log at Warn so it does not trip an Error-level alert.
		connect.CodeCanceled:
		return true
	}
	return false
}

// connectCodeToHTTPStatus maps a Connect error code to its HTTP status integer
// per the Connect protocol specification. Used to populate the numeric status log
// field, so a log-based alert can match on it (e.g. status >= 400).
func connectCodeToHTTPStatus(code connect.Code) int {
	switch code {
	case connect.CodeInvalidArgument,
		connect.CodeFailedPrecondition,
		connect.CodeOutOfRange:
		return 400
	case connect.CodeUnauthenticated:
		return 401
	case connect.CodePermissionDenied:
		return 403
	case connect.CodeNotFound:
		return 404
	case connect.CodeAlreadyExists:
		return 409
	case connect.CodeResourceExhausted:
		return 429
	case connect.CodeUnimplemented:
		return 501
	case connect.CodeUnavailable:
		return 503
	case connect.CodeDeadlineExceeded:
		return 504
	case connect.CodeCanceled:
		// 499 client-closed-request: the caller went away; not a server 5xx.
		return 499
	default:
		return 500
	}
}

// loggingInterceptor emits the single per-request access log for Connect RPCs — unary and
// server-streaming. It installs a request error sink (errctx) so the handler's error
// mapper can record the real domain error; on failure it logs exactly one line carrying
// that real error (not the sanitized client message) plus the procedure, duration, and
// HTTP status.
//
// It is used on both the server and the client interceptor chains, so WrapStreamingClient
// is intentionally a no-op: client-streaming logging is a deliberate, currently-untracked
// scope boundary (only the server-streaming handler is covered), NOT because this
// interceptor is server-side.
type loggingInterceptor struct {
	// logger emits the per-request access log line.
	logger interfaces.Logger
}

// NewLoggingInterceptor returns the single per-request access log for Connect RPCs (unary
// and server-streaming). It installs a request error sink (errctx) so the handler's error
// mapper can record the real domain error; on failure this interceptor logs exactly one
// line carrying that real error (not the sanitized client message) plus the procedure,
// duration, and HTTP status.
//
// 4xx Connect codes (client errors) log at Warn with a numeric status field, so a
// client-error alert can match them; 5xx codes log at Error (with the real
// error_cause), so a server-error alert fires only on genuine faults.
// Successful requests log once at Info. Health probes are not Connect RPCs, so
// they are not logged here.
func NewLoggingInterceptor(logger interfaces.Logger) connect.Interceptor {
	return loggingInterceptor{logger: logger}
}

// logAccess emits the one access line for a completed RPC (unary or stream). Shared by
// both wrap paths; ctx must already carry the errctx sink.
func (i loggingInterceptor) logAccess(
	ctx context.Context, procedure string, duration time.Duration, err error,
) {
	l := i.logger.WithContext(ctx)
	if err == nil {
		l.Info("request", "procedure", procedure, "duration", duration, "status", 200)
		return
	}
	code := connect.CodeUnknown
	var connectErr *connect.Error
	if apperr.As(err, &connectErr) {
		code = connectErr.Code()
	}
	// Prefer the real domain error captured by the error mapper over the sanitized client
	// error, so the log records the true cause (expanded to error_code / error_cause by
	// the structured logger).
	logErr := err
	if realErr := errctx.Captured(ctx); realErr != nil {
		logErr = realErr
	}
	fields := []any{
		"procedure", procedure,
		"duration", duration,
		"status", connectCodeToHTTPStatus(code),
		"error", logErr,
	}
	if is4xxConnectCode(code) {
		l.Warn("request failed", fields...)
	} else {
		l.Error("request failed", fields...)
	}
}

// WrapUnary emits one access line for a unary RPC.
func (i loggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = errctx.WithSink(ctx)
		start := time.Now()
		resp, err := next(ctx, req)
		i.logAccess(ctx, req.Spec().Procedure, time.Since(start), err)
		return resp, err
	}
}

// WrapStreamingClient is a no-op (see the type doc: client-streaming logging is out of
// scope).
func (i loggingInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler emits one access line for a whole server-streaming RPC.
func (i loggingInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = errctx.WithSink(ctx)
		start := time.Now()
		err := next(ctx, conn)
		i.logAccess(ctx, conn.Spec().Procedure, time.Since(start), err)
		return err
	}
}
