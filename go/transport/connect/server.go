// Package connect provides a Connect/gRPC server factory that mounts
// proto-generated handlers on a standard net/http.ServeMux.
package connect

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// HandlerRegistration represents a Connect handler to be registered.
type HandlerRegistration struct {
	// Handler is the HTTP handler serving Connect RPC requests.
	Handler http.Handler

	// Path is the URL path prefix where the handler is mounted.
	Path string
}

// ServerConfig holds server configuration.
type ServerConfig struct {
	// Port is the TCP port the server listens on.
	Port int `yaml:"port" mapstructure:"port"`

	// ReadTimeout is the maximum duration for reading the entire request including the body.
	ReadTimeout time.Duration `yaml:"read_timeout" mapstructure:"read_timeout"`

	// ReadHeaderTimeout is the maximum duration for reading request headers.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" mapstructure:"read_header_timeout"`

	// WriteTimeout is the maximum duration before timing out writes of the response.
	WriteTimeout time.Duration `yaml:"write_timeout" mapstructure:"write_timeout"`

	// IdleTimeout is the maximum duration to wait for the next request on a keep-alive
	// connection.
	IdleTimeout time.Duration `yaml:"idle_timeout" mapstructure:"idle_timeout"`

	// MaxHeaderBytes controls the maximum number of bytes the server reads parsing
	// request headers.
	MaxHeaderBytes int `yaml:"max_header_bytes" mapstructure:"max_header_bytes"`

	// ShutdownTimeout is the maximum duration to wait for in-flight requests during
	// graceful shutdown.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" mapstructure:"shutdown_timeout"`
}

// DefaultServerConfig returns sensible server defaults.
func DefaultServerConfig(port int) ServerConfig {
	return ServerConfig{
		Port:              port,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
		ShutdownTimeout:   10 * time.Second,
	}
}

// Server wraps an HTTP server serving Connect and gRPC handlers.
type Server struct {
	// httpServer holds the underlying net/http server, published (Store) only once the
	// listener is bound and loaded (Load) by Stop/Shutdown/Readiness/Liveness. An atomic
	// pointer because Start writes it while a Kubernetes probe goroutine may read it
	// concurrently, and because it must be nil until the bind succeeds so Readiness does
	// not report ready before the server is actually listening.
	httpServer atomic.Pointer[http.Server]

	// mux is the HTTP request multiplexer for routing handlers.
	mux *http.ServeMux

	// logger is the structured logger for server lifecycle events.
	logger interfaces.Logger

	// cfg holds the server configuration (port, timeouts, limits).
	cfg ServerConfig
}

// NewServer creates a new Connect server with the given configuration.
func NewServer(cfg ServerConfig, logger interfaces.Logger) *Server {
	mux := http.NewServeMux()

	// Health endpoints
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &Server{
		mux:    mux,
		logger: logger,
		cfg:    cfg,
	}
}

// Register adds a Connect handler registration to the server.
func (s *Server) Register(reg HandlerRegistration) {
	s.mux.Handle(reg.Path, reg.Handler)
}

// RegisterFunc adds an http.HandlerFunc to the server.
func (s *Server) RegisterFunc(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Start binds the listener and begins serving in the background, returning once the
// server is listening (or immediately with a bind error). It does NOT block for the
// serve lifetime — call Stop (or Shutdown) to drain the server; the caller blocks on
// its own shutdown signal. This satisfies the interfaces.Lifecycle "return when
// ready" contract so a lifecycle.Manager can coordinate it.
func (s *Server) Start(ctx context.Context) error {
	// h2c (HTTP/2 Cleartext) via net/http's native Protocols; the
	// golang.org/x/net/http2/h2c handler wrapper is deprecated.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		Protocols:         protocols,
		ReadTimeout:       s.cfg.ReadTimeout,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		WriteTimeout:      s.cfg.WriteTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
		MaxHeaderBytes:    s.cfg.MaxHeaderBytes,
		// BaseContext deliberately roots request contexts in context.Background(), NOT the
		// ctx passed to Start: on SIGTERM the caller cancels its ctx, and Shutdown already
		// drains in-flight requests within ShutdownTimeout — inheriting the cancellable ctx
		// would abort those in-flight requests immediately and defeat the graceful drain.
		BaseContext: func(_ net.Listener) context.Context { return context.Background() },
	}

	// Bind the listener synchronously so a bind failure (port already in use) surfaces from
	// Start; publish the server (Store) only AFTER the bind succeeds so Readiness reports
	// ready only once we are actually listening. Then serve in the background so Start
	// returns once the server is listening (the interfaces.Lifecycle "return when ready"
	// contract, so a lifecycle.Manager can start it in sequence without hanging).
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			"connect server listen on "+addr,
		)
	}
	s.httpServer.Store(srv)

	s.logger.Info("Connect server starting", "port", s.cfg.Port)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("connect server serve error", "error", err)
		}
	}()
	return nil
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown() error {
	srv := s.httpServer.Load()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
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

// Mux returns the underlying ServeMux for direct handler registration.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}
