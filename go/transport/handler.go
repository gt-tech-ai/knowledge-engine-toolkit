// Package transport is the controller tier: the edge every inbound request enters by —
// a REST or RPC handler. Tier orders the tier's layers, switched and tuned per
// controller by the decoration config's controllers tier (decoration.controllers, built
// by decorate.Decorations); Base runs a controller's handlers through it. Protocol
// pieces live in the sub-packages.
//
// # Sub-packages
//
//   - rest: HTTP response helpers (BaseController), request-to-handler adapter (Adapt)
//   - rpc: Connect/gRPC error mapping (ToConnectError, FromRPCError)
//   - jsonapi: the JSON:API REST response middleware
//   - docs: OpenAPI spec and Redoc handlers
package transport

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// HandlerFunc is a generic request handler function that processes a request
// and returns a response. This is the transport-layer equivalent of
// Pipeline/Workflow Execute, applied to individual handlers.
//
// HandlerFunc is protocol-agnostic: it works with both REST and RPC transports.
// Base.Handler wraps one with the controller tier.
//
// Example:
//
//	handler := func(ctx context.Context, req LoginRequest) (LoginResponse, error) {
//	    // Validate, call workflow, serialize
//	    return LoginResponse{Token: "..."}, nil
//	}
type HandlerFunc[Req any, Resp any] func(context.Context, Req) (Resp, error)

// Tier is the controller decoration tier, outermost first: each request gets its server
// span, metrics and log line around everything else, so a panic, a shed or a denial is
// still observed (as the edge, a failure is logged at Warn for a client fault and at
// Error otherwise); recovery turns a panic into an INTERNAL error; the rate limit and the
// shedding bulkhead reject excess load before any work; auth authorizes
// "<controller>.<op>"; and the timeout bounds the handler.
func Tier() decorate.Tier {
	return decorate.Tier{
		Name:     "controller",
		SpanKind: interfaces.SpanKindServer,
		Edge:     true,
		Order: []decorate.Layer{
			decorate.LayerTracing,
			decorate.LayerMetrics,
			decorate.LayerLogging,
			decorate.LayerRecovery,
			decorate.LayerRateLimit,
			decorate.LayerBulkhead,
			decorate.LayerAuth,
			decorate.LayerTimeout,
		},
	}
}

// Base runs one controller's handlers through the controller tier. Its embedded Exec
// runs a single operation; Handler decorates a whole HandlerFunc. The zero Base runs
// handlers undecorated.
type Base struct {
	// Subject runs the controller's operations; it supplies Name and Exec.
	decorate.Subject
}

// NewBase returns the Base for the controller named name over deps.
func NewBase(name string, deps decorate.Deps) Base {
	return Base{Subject: Tier().Subject(name, deps)}
}

// Handler returns fn run through the tier as the operation op.
func (b Base) Handler[Req, Resp any](
	op string,
	fn HandlerFunc[Req, Resp],
) HandlerFunc[Req, Resp] {
	return func(ctx context.Context, req Req) (Resp, error) {
		return b.Exec(ctx, op, func(ctx context.Context) (Resp, error) {
			return fn(ctx, req)
		})
	}
}
