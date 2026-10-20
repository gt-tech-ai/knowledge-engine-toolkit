// Package mcp is a Model Context Protocol front-end for an operations registry
// (execution/ops): it advertises one tool per operation the registry admits and runs
// each tool call through the registry, a bounded operation inline and a long-running
// one as a background run (execution/runs) that the runs.* tools observe and cancel.
// ServeStdio serves it on stdin and stdout.
package mcp

import (
	"context"
	"io"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/runs"
)

// Registry is the slice of the operations registry the MCP server adapts: the admitted
// operations, running one by name, and reserving the working tree for a background run.
//
// interface-composition exemption — the consumer-side contract between the MCP
// transport and execution/ops.Registry, with no core interface to embed beyond the run
// table's Registry.
type Registry interface {
	runs.Registry
	// List returns the operations the front-end's policy admits, sorted by name.
	List() []interfaces.Operation
	// Run runs the admitted operation named name with a decoded input.
	Run(
		ctx context.Context,
		name string,
		input any,
		obs interfaces.ExecutionObserver,
	) error
}

// Options names the server in the MCP initialize handshake and bounds what its tool
// calls return and keep.
type Options struct {
	// RunDir receives each tool call's and background run's step logs, and mints the
	// background runs' ids.
	RunDir RunDir
	// Name is the server's implementation name (the serving binary's name).
	Name string
	// Version is the server's implementation version (the serving binary's version).
	Version string
	// Settings bounds each tool result's failed-step tails and the run-log history.
	Settings types.MCPSettings
}

// Server serves the registry's operations as MCP tools over one transport at a time.
// Its background runs end with the first Serve, so a Server serves one session.
type Server struct {
	// runs receives each tool call's step logs.
	runs RunDir
	// reg runs each tool call.
	reg Registry
	// table holds the background runs of the long-running tools.
	table *runs.Table
	// srv is the SDK server carrying one tool per admitted operation.
	srv *sdkmcp.Server
	// settings bounds each tool result's failed-step tails.
	settings types.MCPSettings
}

// New builds a server advertising one tool per operation reg lists — a long-running
// operation's tool starts a background run, and the runs.* tools that follow those runs
// are added only when reg lists at least one; the run table remembers only the newest
// Settings.RunRetention finished runs. It does no I/O. It fails with CodeInvalidInput on
// invalid Settings or a zero RunDir (one not built by NewRunDir), and with CodeInternal
// when an operation's input schema is not a JSON object schema (which the SDK would
// otherwise panic on).
func New(reg Registry, opts Options) (*Server, error) {
	if err := opts.Settings.Validate(); err != nil {
		return nil, err
	}
	if opts.RunDir.root == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"mcp: a run directory is required",
		)
	}
	s := &Server{
		reg:      reg,
		settings: opts.Settings,
		runs:     opts.RunDir,
		srv: sdkmcp.NewServer(
			&sdkmcp.Implementation{Name: opts.Name, Version: opts.Version},
			nil,
		),
		table: runs.New(reg, opts.RunDir.NewRunID,
			func(snap runs.Snapshot) ([]string, error) {
				return opts.RunDir.WriteSteps(snap.ID, flatten(snap.Steps))
			}, opts.Settings.RunRetention),
	}
	background := false
	for _, op := range reg.List() {
		tool, err := toolFor(op)
		if err != nil {
			return nil, err
		}
		if op.Descriptor().Lifecycle == types.OpLifecycleLongRunning {
			s.srv.AddTool(tool, s.startHandler(op))
			background = true
			continue
		}
		s.srv.AddTool(tool, s.handler(op))
	}
	if background {
		s.addRunsTools()
	}
	return s, nil
}

// Serve prunes the run directory to the newest Settings.RunRetention runs, then runs the
// server over t until the client disconnects or ctx is cancelled, then cancels every
// background run still active and waits for them to stop, so no operation outlives the
// session. A failed prune is returned before the session starts.
func (s *Server) Serve(ctx context.Context, t sdkmcp.Transport) error {
	if err := s.runs.Prune(s.settings.RunRetention); err != nil {
		return err
	}
	return s.session(ctx, t)
}

// session runs one session over t, then stops every background run it started.
func (s *Server) session(ctx context.Context, t sdkmcp.Transport) error {
	err := s.srv.Run(ctx, t)
	s.table.CancelAll()
	s.table.Wait()
	return apperr.Wrap(err, apperr.CodeInternal, "Server.Run")
}

// ServeStdio prunes the run directory like Serve, then serves over newline-delimited
// JSON on in and out until the client closes in or ctx is cancelled (SIGINT / SIGTERM);
// either is a clean shutdown and returns nil. A failed prune is returned as it is; any
// other transport failure returns CodeInternal.
func (s *Server) ServeStdio(
	ctx context.Context,
	in io.ReadCloser,
	out io.WriteCloser,
) error {
	if err := s.runs.Prune(s.settings.RunRetention); err != nil {
		return err
	}
	err := s.session(ctx, &sdkmcp.IOTransport{Reader: in, Writer: out})
	if err == nil || isShutdown(err) {
		return nil
	}
	return apperr.Wrap(err, apperr.CodeInternal, "mcp server: transport failed")
}

// isShutdown reports whether err is how a session ends normally: the client closing the
// stream, the connection closing, or the context being cancelled.
func isShutdown(err error) bool {
	for _, end := range []error{
		io.EOF, io.ErrClosedPipe, sdkmcp.ErrConnectionClosed, context.Canceled,
	} {
		if apperr.StdIs(err, end) {
			return true
		}
	}
	return false
}
