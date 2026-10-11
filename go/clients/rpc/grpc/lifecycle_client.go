package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Client is a lifecycle-managed gRPC client connection: Start dials the target,
// Stop closes the conn, and Liveness/Readiness reflect the connection state. It
// satisfies interfaces.Client so a lifecycle.Manager coordinates its startup and
// shutdown; Conn() exposes the *grpc.ClientConn for building generated stubs.
type Client struct {
	// conn is the underlying gRPC connection, dialed by Start and closed by Stop.
	conn *grpc.ClientConn
	// opts are the dial options applied when Start dials the target.
	opts []grpc.DialOption
	// cfg is the client configuration (target, TLS, timeouts).
	cfg ClientConfig
}

// compile-time check: Client is a lifecycle-managed, health-checkable client.
var _ interfaces.Client = (*Client)(nil)

// NewLifecycleClient builds a lifecycle-managed gRPC client. The connection is not
// dialed until Start.
func NewLifecycleClient(cfg ClientConfig, opts ...grpc.DialOption) *Client {
	return &Client{cfg: cfg, opts: opts}
}

// Start dials the target. grpc.NewClient is lazy, so this does not block on the
// first successful connect; a down target surfaces via Readiness.
func (c *Client) Start(_ context.Context) error {
	conn, err := NewClient(c.cfg, c.opts...)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "dial grpc client")
	}
	c.conn = conn
	return nil
}

// Stop closes the connection.
func (c *Client) Stop(_ context.Context) error {
	if c.conn != nil {
		return apperr.Wrap(
			c.conn.Close(),
			apperr.CodeInternal,
			"close grpc connection",
		)
	}
	return nil
}

// Liveness reports whether the client has been started.
func (c *Client) Liveness(_ context.Context) error {
	if c.conn == nil {
		return apperr.Internal("grpc client not started")
	}
	return nil
}

// Readiness reports whether the connection is out of a terminal failure state.
func (c *Client) Readiness(_ context.Context) error {
	if c.conn == nil {
		return apperr.Internal("grpc client not started")
	}
	if s := c.conn.GetState(); s == connectivity.TransientFailure ||
		s == connectivity.Shutdown {
		return apperr.Internal("grpc connection not ready: " + s.String())
	}
	return nil
}

// Conn returns the underlying connection for building generated stubs. The return
// type is grpc.ClientConnInterface — the type generated stub constructors accept —
// so the rpc.RPCClient contract does not leak the concrete *grpc.ClientConn.
func (c *Client) Conn() grpc.ClientConnInterface {
	return c.conn
}
