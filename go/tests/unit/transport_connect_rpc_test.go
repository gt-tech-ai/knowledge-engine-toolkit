package unit_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// testProcedure is the procedure the interceptor tests' RPCs run as.
const testProcedure = "/test.v1.TestService/Do"

// testRequest is one RPC an interceptor test drives through a connect.Server: its
// procedure and shape, and the CallInfo whose request header the interceptors read and
// whose response header they write.
type testRequest struct {
	// info is the RPC's CallInfo, handed to connect.Server.Call.
	info *connect.CallInfo

	// spec names the procedure and its stream type.
	spec connect.Spec
}

// testResponse is the value a test handler returns.
type testResponse struct{}

// newTestRequest returns a unary RPC with an empty request header.
func newTestRequest() *testRequest {
	return newTestRequestWithHeaders(nil)
}

// newTestRequestWithHeaders returns a unary RPC whose request header carries headers.
func newTestRequestWithHeaders(headers map[string]string) *testRequest {
	req := &testRequest{
		info: new(connect.CallInfo),
		spec: connect.Spec{
			StreamType:       connect.StreamTypeUnary,
			IdempotencyLevel: connect.IdempotencyUnknown,
			Schema:           nil,
			Procedure:        testProcedure,
		},
	}
	for k, v := range headers {
		req.info.RequestHeader().Set(k, v)
	}

	return req
}

// newTestStream returns a server-streaming RPC whose request header carries headers.
func newTestStream(headers map[string]string) *testRequest {
	req := newTestRequestWithHeaders(headers)

	req.spec.StreamType = connect.StreamTypeServer

	return req
}

// Header returns the RPC's request header.
func (r *testRequest) Header() *connect.Header { return r.info.RequestHeader() }

// ResponseHeader returns the response header the interceptors wrote.
func (r *testRequest) ResponseHeader() *connect.Header { return r.info.ResponseHeader() }

// Spec returns the RPC's procedure and shape.
func (r *testRequest) Spec() connect.Spec { return r.spec }

// newTestResponse returns a non-nil handler result.
func newTestResponse() any { return &testResponse{} }

// testHandler is a unary test handler: it sees the context the interceptors built.
type testHandler func(ctx context.Context, req *testRequest) (any, error)

// wrapUnary returns a function that runs a request through a connect.Server built with
// interceptor, whose one method calls handler; it returns the handler's result and the
// error the chain returned.
func wrapUnary(
	interceptor connect.ServerInterceptor,
	handler testHandler,
) func(context.Context, *testRequest) (any, error) {
	return func(ctx context.Context, req *testRequest) (any, error) {
		var resp any

		err := serveRPC(
			ctx,
			[]connect.ServerInterceptor{interceptor},
			req,
			func(ctx context.Context) error {
				var err error

				resp, err = handler(ctx, req)

				return err
			},
		)

		return resp, err
	}
}

// serveRPC registers req's procedure on a connect.Server built with ics, whose handler
// is body, and dispatches req through it, returning the error the chain returned.
func serveRPC(
	ctx context.Context,
	ics []connect.ServerInterceptor,
	req *testRequest,
	body func(ctx context.Context) error,
) error {
	server := connect.NewServer(ics...)
	server.Register(connect.Method{
		Spec: req.spec,
		Handler: func(ctx context.Context, _ connect.Spec, _ connect.ServerStream) error {
			return body(ctx)
		},
	})

	return apperr.Wrap(
		server.Call(ctx, req.spec.Procedure, req.info, nil),
		apperr.CodeUnknown,
		"dispatch the test RPC",
	)
}

// wrapStream returns a function that runs a streaming request through a connect.Server
// built with interceptor, whose one method calls handler.
func wrapStream(
	interceptor connect.ServerInterceptor,
	handler func(ctx context.Context, req *testRequest) error,
) func(context.Context, *testRequest) error {
	return func(ctx context.Context, req *testRequest) error {
		return serveRPC(
			ctx,
			[]connect.ServerInterceptor{interceptor},
			req,
			func(ctx context.Context) error { return handler(ctx, req) },
		)
	}
}

// assertCode asserts that err carries the Connect code want.
func assertCode(t *testing.T, want connect.Code, err error) {
	t.Helper()

	assert.Equal(t, want, connect.CodeOf(err), "connect code of %v", err)
}
