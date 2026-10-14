// Package stub is the zero-infrastructure ServerTransport: handlers register on a mux
// that is never served, so a graph boots and its routes can be exercised in process
// (httptest) without opening a port.
package stub

import (
	"context"
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Server is the unserved ServerTransport.
type Server struct {
	// mux holds the registered handlers.
	mux *http.ServeMux
}

var _ interfaces.ServerTransport = (*Server)(nil)

// New returns a stub server over an empty mux.
func New() *Server { return &Server{mux: http.NewServeMux()} }

// Start opens no listener.
func (*Server) Start(context.Context) error { return nil }

// Stop has nothing to drain.
func (*Server) Stop(context.Context) error { return nil }

// Liveness always passes.
func (*Server) Liveness(context.Context) error { return nil }

// Readiness always passes.
func (*Server) Readiness(context.Context) error { return nil }

// RegisterFunc registers handler for pattern on the mux.
func (s *Server) RegisterFunc(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Mux returns the mux the handlers are registered on.
func (s *Server) Mux() *http.ServeMux { return s.mux }
