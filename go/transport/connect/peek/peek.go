// Package peek lets a Connect server interceptor read a unary request before the
// handler does: Unary receives the request off the stream and returns a stream that
// replays it, so the interceptor can inspect (or rewrite) the message and still hand the
// handler the call.
package peek

import (
	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// Unary reads the request off stream as the input message spec's method descriptor
// names, and returns it with a stream whose first Receive replays it. Changes the caller
// makes to the returned message before the handler's Receive are what the handler sees.
// A procedure with no protobuf method schema, or an input type missing from the global
// registry, fails with CodeInternal; a receive failure comes back coded
// CodeInvalidInput.
func Unary(
	spec connect.Spec,
	stream connect.ServerStream,
) (proto.Message, connect.ServerStream, error) {
	method, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok {
		return nil, nil, connect.NewError(connect.CodeInternal, apperr.New(
			apperr.CodeInternal, "procedure carries no protobuf method schema",
		).Error())
	}

	input, err := protoregistry.GlobalTypes.FindMessageByName(method.Input().FullName())
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, apperr.Wrap(
			err, apperr.CodeInternal, "resolve request message type",
		).Error())
	}

	req := input.New().Interface()

	err = stream.Receive(req)
	if err != nil {
		return nil, nil, apperr.Wrap(err, apperr.CodeInvalidInput, "receive request")
	}

	return req, &replayStream{ServerStream: stream, request: req}, nil
}

// replayStream hands the handler the request the interceptor already read; every later
// Receive goes to the underlying stream.
type replayStream struct {
	connect.ServerStream

	// request is the already-read request, nil once replayed.
	request proto.Message
}

// Receive merges the already-read request into msg on the first call and reads from the
// underlying stream after that.
func (s *replayStream) Receive(msg any) error {
	if s.request == nil {
		return apperr.Wrap(
			s.ServerStream.Receive(msg), apperr.CodeInvalidInput, "receive request",
		)
	}

	dst, ok := msg.(proto.Message)
	if !ok {
		return connect.NewError(connect.CodeInternal,
			apperr.New(apperr.CodeInternal, "request is not a protobuf message").Error())
	}

	proto.Reset(dst)
	proto.Merge(dst, s.request)

	s.request = nil

	return nil
}
