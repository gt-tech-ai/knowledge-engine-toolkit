package unit_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
)

// validSpanCtx returns a context carrying a fixed, valid W3C span context and the
// traceparent string it serializes to — so a test can assert the interceptor returns
// exactly that id to the caller.
func validSpanCtx(t *testing.T) (ctx context.Context, traceParent string) {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx = trace.ContextWithSpanContext(context.Background(), sc)
	return ctx, tracer.TraceParentFromContext(ctx)
}

// anySpan returns a mock Span that accepts the attribute/status/end calls the tracing
// interceptor makes, without asserting on them (this suite is about the response header).
func anySpan(ctrl *gomock.Controller) *mocks.MockSpan {
	s := mocks.NewMockSpan(ctrl)
	s.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	s.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	s.EXPECT().RecordError(gomock.Any()).AnyTimes()
	s.EXPECT().End().AnyTimes()
	return s
}

// TestTracingInterceptor_UnarySetsTraceResponseHeader tests that a successful unary RPC
// returns the active span's trace context to the caller in the `traceresponse` header.
//
// Why this test is important:
//   - A telemetry round-trip proof needs the client to learn its trace id;
//     without this header a caller cannot correlate its request to the spans it produced.
//
// What it tests:
//   - After the handler runs, the response carries `traceresponse` equal to the W3C
//
// traceparent of the server span.
func TestTracingInterceptor_UnarySetsTraceResponseHeader(t *testing.T) {
	ctrl := gomock.NewController(t)
	spanCtx, wantTraceParent := validSpanCtx(t)

	tr := mocks.NewMockTracer(ctrl)
	tr.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	handler := interceptors.TracingInterceptor(tr).WrapUnary(
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&emptyMessage{}), nil
		},
	)

	resp, err := handler(context.Background(), connect.NewRequest[*emptyMessage](nil))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, wantTraceParent, resp.Header().Get("traceresponse"))
}

// TestTracingInterceptor_UnaryNoHeaderWithoutValidSpan tests that no `traceresponse`
// header is emitted when there is no valid active span context.
//
// Why this test is important:
//   - The header must carry a real trace id or nothing at all — never an empty/invalid
//     value that a client might trust.
//
// What it tests:
//   - With a tracer that yields no valid span context, the response has no
//     `traceresponse`.
func TestTracingInterceptor_UnaryNoHeaderWithoutValidSpan(t *testing.T) {
	ctrl := gomock.NewController(t)

	tr := mocks.NewMockTracer(ctrl)
	// Start returns a plain context with no span context in it.
	tr.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(context.Background(), anySpan(ctrl))

	handler := interceptors.TracingInterceptor(tr).WrapUnary(
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&emptyMessage{}), nil
		},
	)

	resp, err := handler(context.Background(), connect.NewRequest[*emptyMessage](nil))
	require.NoError(t, err)
	assert.Empty(t, resp.Header().Get("traceresponse"))
}

// TestTracingInterceptor_UnaryNilResponseNoPanic tests that the error path (a nil
// response) does not panic when stamping the header.
//
// Why this test is important:
//   - Connect returns a nil response alongside a non-nil error; dereferencing it to set
//     a header would panic and turn every failing RPC into a 500 from the interceptor.
//
// What it tests:
//   - A handler returning (nil, err) surfaces the error with a nil response and no panic.
func TestTracingInterceptor_UnaryNilResponseNoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	spanCtx, _ := validSpanCtx(t)

	tr := mocks.NewMockTracer(ctrl)
	tr.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	wantErr := apperr.Sentinel("boom")
	handler := interceptors.TracingInterceptor(tr).WrapUnary(
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, wantErr
		},
	)

	resp, err := handler(context.Background(), connect.NewRequest[*emptyMessage](nil))
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, resp)
}

// TestTracingInterceptor_StreamingSetsTraceResponseBeforeHandler tests that a
// server-streaming RPC stamps `traceresponse` on the response header BEFORE the handler
// runs.
//
// Why this test is important:
//   - Streaming response headers flush with the first Send, so the trace id must be set
//     before the handler starts producing frames — otherwise the client never receives
//     it.
//
// What it tests:
//   - The `traceresponse` header equals the server span's traceparent and is already
//     present when the handler executes.
func TestTracingInterceptor_StreamingSetsTraceResponseBeforeHandler(t *testing.T) {
	ctrl := gomock.NewController(t)
	spanCtx, wantTraceParent := validSpanCtx(t)

	tr := mocks.NewMockTracer(ctrl)
	tr.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	respHeader := http.Header{}
	conn := mocks.NewMockStreamingHandlerConn(ctrl)
	conn.EXPECT().
		Spec().
		Return(connect.Spec{Procedure: "/test.v1.StreamService/Stream"}).
		AnyTimes()
	conn.EXPECT().RequestHeader().Return(http.Header{}).AnyTimes()
	conn.EXPECT().ResponseHeader().Return(respHeader).AnyTimes()

	var headerAtHandler string
	handler := interceptors.TracingInterceptor(tr).WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			headerAtHandler = respHeader.Get("traceresponse") // captured before any Send
			return nil
		},
	)

	err := handler(context.Background(), conn)
	require.NoError(t, err)
	assert.Equal(
		t,
		wantTraceParent,
		headerAtHandler,
		"header must be set before the handler runs",
	)
}
