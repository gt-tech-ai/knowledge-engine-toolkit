package interceptors

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// statusClientClosed is the non-standard status of a request the client abandoned.
const statusClientClosed = 499

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
	if status, ok := clientFaultStatus(code); ok {
		return status
	}

	switch code {
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeCanceled:
		// 499 client-closed-request: the caller went away; not a server 5xx.
		return statusClientClosed
	default:
		return http.StatusInternalServerError
	}
}

// clientFaultStatus maps the codes that blame the request to their 4xx status.
func clientFaultStatus(code connect.Code) (int, bool) {
	switch code {
	case connect.CodeInvalidArgument,
		connect.CodeFailedPrecondition,
		connect.CodeOutOfRange:
		return http.StatusBadRequest, true
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized, true
	case connect.CodePermissionDenied:
		return http.StatusForbidden, true
	case connect.CodeNotFound:
		return http.StatusNotFound, true
	case connect.CodeAlreadyExists:
		return http.StatusConflict, true
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests, true
	default:
		return 0, false
	}
}

// loggingInterceptor emits the single per-request access log for Connect RPCs — unary and
// server-streaming. It installs a request error sink (core/errors.WithSink) so the
// handler's error mapper can record the real domain error; on failure it logs exactly one
// line carrying that real error (not the sanitized client message) plus the procedure,
// duration, and HTTP status.
//
// It is a server-side interceptor (a client's calls are logged by the client tier, see
// ClientOptions), so WrapStreamingClient is a no-op.
type loggingInterceptor struct {
	// logger emits the per-request access log line.
	logger interfaces.Logger
}

// NewLoggingInterceptor returns the single per-request access log for Connect RPCs (unary
// and server-streaming). It installs a request error sink (core/errors.WithSink) so the
// handler's error mapper can record the real domain error; on failure this interceptor
// logs exactly one line carrying that real error (not the sanitized client message) plus
// the procedure, duration, and HTTP status.
//
// 4xx Connect codes (client errors) log at Warn with a numeric status field, so a
// client-error alert can match them; 5xx codes log at Error (with the real
// error_cause), so a server-error alert fires only on genuine faults.
// Successful requests log once at Info. Health probes are not Connect RPCs, so
// they are not logged here.
func NewLoggingInterceptor(logger interfaces.Logger) connect.Interceptor {
	return loggingInterceptor{logger: logger}
}

// WrapUnary emits one access line for a unary RPC.
func (i loggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = apperr.WithSink(ctx)

		start := time.Now()
		resp, err := next(ctx, req)
		i.logAccess(ctx, req.Spec().Procedure, time.Since(start), err)

		return resp, err
	}
}

// WrapStreamingClient is a no-op — this is a server-side interceptor.
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
		ctx = apperr.WithSink(ctx)

		start := time.Now()
		err := next(ctx, conn)
		i.logAccess(ctx, conn.Spec().Procedure, time.Since(start), err)

		return err
	}
}

// logAccess emits the one access line for a completed RPC (unary or stream). Shared by
// both wrap paths; ctx must already carry the request error sink (core/errors.WithSink).
func (i loggingInterceptor) logAccess(
	ctx context.Context, procedure string, duration time.Duration, err error,
) {
	l := i.logger.WithContext(ctx)
	if err == nil {
		l.Info(
			"request",
			"procedure",
			procedure,
			"duration",
			duration,
			"status",
			http.StatusOK,
		)

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

	realErr := apperr.Captured(ctx)
	if realErr != nil {
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
