package connect

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time check: the Connect server satisfies the transport tier's contract
// (interfaces.ServerTransport, which embeds interfaces.Client): Start (serve loop)
// and Stop (graceful shutdown) let a lifecycle.Manager coordinate it,
// Liveness/Readiness answer Kubernetes probes, and RegisterFunc/Mux mount handlers.
var _ interfaces.ServerTransport = (*Server)(nil)

// Stop gracefully shuts down the server, honoring ctx as the drain deadline. It is
// the interfaces.Lifecycle form of Shutdown, so a lifecycle.Manager stops the server
// with the same contract it uses for every other client.
func (s *Server) Stop(ctx context.Context) error {
	srv := s.httpServer.Load()
	if srv == nil {
		return nil
	}
	s.logger.Info("Connect server shutting down")
	if err := srv.Shutdown(ctx); err != nil {
		return apperr.Wrap(
			err,
			apperr.ContextCode(err, apperr.CodeInternal),
			"shut down connect server",
		)
	}
	return nil
}

// Liveness reports the server process is up — a lightweight self-check that does
// not depend on external systems.
func (s *Server) Liveness(_ context.Context) error { return nil }

// Readiness reports whether the server has begun listening (Start bound the
// listener); until then it is not ready to serve traffic.
func (s *Server) Readiness(_ context.Context) error {
	if s.httpServer.Load() == nil {
		return apperr.Internal("connect server not started")
	}
	return nil
}
