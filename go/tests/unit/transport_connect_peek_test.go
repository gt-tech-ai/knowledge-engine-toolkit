package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/peek"
)

// peekSpec returns the spec of a unary method peek.v1.PeekService/Echo whose schema
// is a descriptor built here: it takes a google.protobuf.StringValue and returns a
// google.protobuf.Empty, both in the global registry, so peek resolves the input the
// way it would for a generated service.
func peekSpec(t *testing.T) connect.Spec {
	t.Helper()

	method := new(descriptorpb.MethodDescriptorProto)

	method.Name = new("Echo")
	method.InputType = new(".google.protobuf.StringValue")
	method.OutputType = new(".google.protobuf.Empty")

	service := new(descriptorpb.ServiceDescriptorProto)

	service.Name = new("PeekService")
	service.Method = []*descriptorpb.MethodDescriptorProto{method}

	fileProto := new(descriptorpb.FileDescriptorProto)

	fileProto.Name = new("peek/v1/peek.proto")
	fileProto.Package = new("peek.v1")
	fileProto.Syntax = new("proto3")
	fileProto.Dependency = []string{
		"google/protobuf/wrappers.proto",
		"google/protobuf/empty.proto",
	}
	fileProto.Service = []*descriptorpb.ServiceDescriptorProto{service}

	file, err := protodesc.NewFile(fileProto, protoregistry.GlobalFiles)
	require.NoError(t, err)

	return connect.Spec{
		StreamType:       connect.StreamTypeUnary,
		IdempotencyLevel: connect.IdempotencyUnknown,
		Schema:           file.Services().Get(0).Methods().Get(0),
		Procedure:        "/peek.v1.PeekService/Echo",
	}
}

// TestPeekUnary_HandlerSeesPeekedRequest tests that an interceptor can read, and
// rewrite, a unary request through peek.Unary before the handler receives it.
//
// Why this test is important:
//   - Connect v2 hands a server interceptor a stream, not a request, so an interceptor
//     that scopes a call by a request field must read the message off the stream. If
//     the replayed stream lost the message, every peeked RPC would reach its handler
//     empty; if it replayed a stale copy, the interceptor's rewrite would be ignored.
//
// What it tests:
//   - Over a real Connect HTTP server, the interceptor reads the request the client sent.
//   - The handler receives the request the interceptor left, including its rewrite.
func TestPeekUnary_HandlerSeesPeekedRequest(t *testing.T) {
	t.Parallel()

	var peeked, handled string

	interceptor := func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			req, replay, err := peek.Unary(spec, stream)
			if err != nil {
				return err
			}

			value, ok := req.(*wrapperspb.StringValue)
			require.True(t, ok, "peek decodes the method's input type")

			peeked = value.GetValue()
			value.Value = "rewritten"

			return next(ctx, spec, replay)
		}
	}

	spec := peekSpec(t)

	server := connect.NewServer(interceptor)
	server.Register(connect.Method{
		Spec: spec,
		Handler: func(_ context.Context, _ connect.Spec, stream connect.ServerStream) error {
			req := new(wrapperspb.StringValue)

			err := stream.Receive(req)
			if err != nil {
				return apperr.Wrap(err, apperr.CodeInvalidInput, "receive request")
			}

			handled = req.GetValue()

			return stream.Send(new(emptypb.Empty))
		},
	})

	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))

	err := client.CallUnary(
		t.Context(), spec, wrapperspb.String("sent"), new(emptypb.Empty),
	)

	require.NoError(t, err)
	assert.Equal(t, "sent", peeked)
	assert.Equal(t, "rewritten", handled)
}

// TestPeekUnary_RejectsSchemalessProcedure tests that peek.Unary refuses a procedure
// whose spec carries no protobuf method descriptor.
//
// Why this test is important:
//   - Without a schema peek cannot know the request type; decoding into a guess would
//     hand an interceptor a wrong message, so the call must fail closed instead.
//
// What it tests:
//   - The error is a Connect CodeInternal and no request or stream is returned.
func TestPeekUnary_RejectsSchemalessProcedure(t *testing.T) {
	t.Parallel()

	req, stream, err := peek.Unary(connect.Spec{
		StreamType:       connect.StreamTypeUnary,
		IdempotencyLevel: connect.IdempotencyUnknown,
		Schema:           nil,
		Procedure:        "/peek.v1.PeekService/Echo",
	}, nil)

	assertCode(t, connect.CodeInternal, err)
	assert.Nil(t, req)
	assert.Nil(t, stream)
}
