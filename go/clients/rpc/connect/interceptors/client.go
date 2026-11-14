// Package interceptors runs a generated Connect client's calls through the client tier
// (client.Tier): ClientInterceptors returns it as a Connect client interceptor for
// connect.NewClient.
package interceptors

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
)

// ClientInterceptors returns the interceptors that run every call of a generated Connect
// client through base, the client tier, so it is decorated like every other client — its
// layers switched and tuned by the clients tier of the decoration config. Pass them to
// connect.NewClient.
//
// A unary call runs as the operation named by its procedure ("pkg.Service/Method"),
// retried when the tier retries and the schema declares the procedure idempotent (an
// idempotency_level of NO_SIDE_EFFECTS or IDEMPOTENT): a procedure of unknown idempotency
// is a write whose first attempt may have landed, so it runs at most once. Connect v2
// opens a unary stream before the request is sent, so the interceptor hands back a stream
// that holds the request and runs the whole exchange (open, send, receive) as one tier
// operation when the response is read; each attempt carries the active trace context in
// its request header. A streaming call only carries the trace context: its open does no
// I/O, so there is nothing to decorate. The *connect.Error a server returns is coded for
// the tier (wire.FromError) and handed back unchanged; a failure the tier produced itself
// becomes a *connect.Error of its code. base must not be nil.
func ClientInterceptors(base *client.Base) []connect.ClientInterceptor {
	tier := *base

	return []connect.ClientInterceptor{
		func(next connect.ClientFunc) connect.ClientFunc {
			return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
				if spec.StreamType != connect.StreamTypeUnary {
					injectTrace(ctx)

					return next(ctx, spec)
				}

				return &unaryStream{
					exchange: func(req, res any) error {
						return exchange(ctx, &tier, spec, next, req, res)
					},
					req: nil,
				}, nil
			}
		},
	}
}

// unaryStream is the ClientStream a unary call gets: Send keeps the request, and Receive
// runs the real exchange through the client tier.
type unaryStream struct {
	// exchange runs the call, bound to its context, procedure and next opener.
	exchange func(req, res any) error

	// req is the request Send was given.
	req any
}

// SendHeaders does nothing: the headers go with each attempt's request.
func (*unaryStream) SendHeaders() error { return nil }

// Send keeps the request for the attempts Receive makes.
func (s *unaryStream) Send(msg any) error {
	s.req = msg

	return nil
}

// CloseSend does nothing: each attempt closes its own send side.
func (*unaryStream) CloseSend() error { return nil }

// Close does nothing: each attempt releases its own stream.
func (*unaryStream) Close() error { return nil }

// Receive runs the unary exchange and reads the response into msg.
func (s *unaryStream) Receive(msg any) error { return s.exchange(s.req, msg) }

// exchange runs one unary call through the tier as the operation named by its
// procedure, at most once when its idempotency is unknown, each attempt over a fresh
// stream from next; a failure the tier returns becomes a *connect.Error.
func exchange(
	ctx context.Context,
	tier *client.Base,
	spec connect.Spec,
	next connect.ClientFunc,
	req, res any,
) error {
	run := tier.Exec[struct{}]
	if spec.IdempotencyLevel == connect.IdempotencyUnknown {
		run = tier.ExecOnce[struct{}]
	}

	_, err := run(
		ctx,
		strings.TrimPrefix(spec.Procedure, "/"),
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, attempt(ctx, spec, next, req, res)
		},
	)
	if err != nil {
		return wire.ToConnect(err)
	}

	return nil
}

// attempt opens one real stream, sends req and reads the response into res, coding any
// failure for the tier (wire.FromError).
func attempt(
	ctx context.Context,
	spec connect.Spec,
	next connect.ClientFunc,
	req, res any,
) (err error) {
	injectTrace(ctx)

	stream, err := next(ctx, spec)
	if err != nil {
		return wire.FromError(err)
	}

	defer func() {
		closeErr := stream.Close()
		if err == nil && closeErr != nil {
			err = wire.FromError(closeErr)
		}
	}()

	err = stream.Send(req)
	if err != nil {
		return wire.FromError(err)
	}

	err = stream.CloseSend()
	if err != nil {
		return wire.FromError(err)
	}

	return wire.FromError(stream.Receive(res))
}

// injectTrace writes ctx's W3C trace context into the call's request header, so the
// server continues this trace.
func injectTrace(ctx context.Context) {
	info, ok := connect.CallInfoForClientContext(ctx)
	if !ok {
		return
	}

	otel.GetTextMapPropagator().Inject(
		ctx,
		wire.HeaderCarrier{Header: info.RequestHeader()},
	)
}
