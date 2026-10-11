// Package transport provides transport-layer abstractions for HTTP and RPC handlers.
//
// HandlerFunc is a generic request handler that can be decorated with logging,
// metrics, and recovery via the decorators sub-package. Protocol-specific
// implementations live in the rest/ and rpc/ sub-packages.
//
// # Sub-packages
//
//   - rest: HTTP response helpers (BaseController), request-to-handler adapter (Adapt)
//   - rpc: Connect/gRPC error mapping (ToConnectError, FromRPCError)
//   - decorators: Fluent decorator composition (HandlerBuilder)
package transport

import "context"

// HandlerFunc is a generic request handler function that processes a request
// and returns a response. This is the transport-layer equivalent of
// Pipeline/Workflow Execute, applied to individual handlers.
//
// HandlerFunc is protocol-agnostic: it works with both REST and RPC transports.
// Use the decorators.HandlerBuilder to wrap handlers with cross-cutting concerns.
//
// Example:
//
//	handler := func(ctx context.Context, req LoginRequest) (LoginResponse, error) {
//	    // Validate, call workflow, serialize
//	    return LoginResponse{Token: "..."}, nil
//	}
type HandlerFunc[Req any, Resp any] func(context.Context, Req) (Resp, error)
