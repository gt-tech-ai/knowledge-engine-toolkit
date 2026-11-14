package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		TraceState: trace.TraceState{},
		Remote:     false,
	})

	ctx = trace.ContextWithSpanContext(context.Background(), spanContext)

	return ctx, tracer.TraceParentFromContext(ctx)
}

// anySpan returns a mock Span that accepts the attribute/status/end calls the tracing
// interceptor makes, without asserting on them (this suite is about the response header).
func anySpan(ctrl *gomock.Controller) *mocks.MockSpan {
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().End().AnyTimes()

	return span
}

// TestTracingInterceptor_UnarySetsTraceResponseHeader tests that a unary RPC returns
// the active span's trace context to the caller in the `traceresponse` header.
//
// Why this test is important:
//   - A telemetry round-trip proof needs the client to learn its trace id;
//     without this header a caller cannot correlate its request to the spans it produced.
//   - A unary response's headers flush when the handler sends its response, so the
//     header must be set before the handler runs.
//
// What it tests:
//   - The response header carries `traceresponse` equal to the W3C traceparent of the
//     server span, already when the handler runs.
func TestTracingInterceptor_UnarySetsTraceResponseHeader(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	spanCtx, wantTraceParent := validSpanCtx(t)

	tracerMock := mocks.NewMockTracer(ctrl)
	tracerMock.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	var headerAtHandler string

	req := newTestRequest()
	handler := wrapUnary(
		interceptors.TracingInterceptor(tracerMock),
		func(context.Context, *testRequest) (any, error) {
			headerAtHandler = req.ResponseHeader().Get(textTraceresponse)

			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, wantTraceParent, headerAtHandler)
	assert.Equal(t, wantTraceParent, req.ResponseHeader().Get(textTraceresponse))
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
	t.Parallel()

	ctrl := gomock.NewController(t)

	tracerMock := mocks.NewMockTracer(ctrl)
	// Start returns a plain context with no span context in it.
	tracerMock.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(context.Background(), anySpan(ctrl))

	req := newTestRequest()
	handler := wrapUnary(
		interceptors.TracingInterceptor(tracerMock),
		func(context.Context, *testRequest) (any, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, req.ResponseHeader().Has(textTraceresponse))
}

// TestTracingInterceptor_UnaryErrorKeepsHandlerError tests that a failing unary RPC
// returns the handler's own error and still carries the trace id.
//
// Why this test is important:
//   - The tracing layer must never replace the handler's status; and a failed call is
//     the one a caller most needs to correlate with its spans.
//
// What it tests:
//   - A handler error is returned as is (errors.Is), and `traceresponse` is set.
func TestTracingInterceptor_UnaryErrorKeepsHandlerError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	spanCtx, wantTraceParent := validSpanCtx(t)

	currentTracer := mocks.NewMockTracer(ctrl)
	currentTracer.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	wantErr := apperr.Sentinel(textBoom)
	req := newTestRequest()
	handler := wrapUnary(
		interceptors.TracingInterceptor(currentTracer),
		func(context.Context, *testRequest) (any, error) {
			return nil, wantErr
		},
	)

	_, err := handler(context.Background(), req)
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, wantTraceParent, req.ResponseHeader().Get(textTraceresponse))
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
	t.Parallel()

	ctrl := gomock.NewController(t)
	spanCtx, wantTraceParent := validSpanCtx(t)

	currentTracer := mocks.NewMockTracer(ctrl)
	currentTracer.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(spanCtx, anySpan(ctrl))

	var headerAtHandler string

	stream := newTestStream(nil)
	handler := wrapStream(
		interceptors.TracingInterceptor(currentTracer),
		func(context.Context, *testRequest) error {
			// Captured before any Send.
			headerAtHandler = stream.ResponseHeader().Get(textTraceresponse)

			return nil
		},
	)

	err := handler(context.Background(), stream)
	require.NoError(t, err)
	assert.Equal(
		t,
		wantTraceParent,
		headerAtHandler,
		"header must be set before the handler runs",
	)
}

const (
	// textTraceresponse is the traceresponse header's name.
	textTraceresponse = "Traceresponse"
)
