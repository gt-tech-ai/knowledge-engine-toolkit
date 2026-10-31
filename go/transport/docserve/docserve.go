// Package docserve runs long-lived local documentation servers until the context ends:
// Go pkgsite and Python pdoc as supervised processes, and a workspace's OpenAPI specs
// through an in-process Redoc HTTP server (transport/docs).
package docserve

import (
	"context"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/workspace/python"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/docs"
)

// shutdownTimeout bounds the API server's graceful shutdown once the context ends.
const shutdownTimeout = 5 * time.Second

// indexPage is the page listing the specs' Redoc pages when more than one service has a
// spec; html/template escapes every name.
const indexPage = `<h1>API Documentation</h1><ul>` +
	`{{range .}}<li><a href="/{{.}}">{{.}}</a></li>{{end}}</ul>`

// Config locates what the servers document and where they listen. Every field is
// required by the server that reads it.
type Config struct {
	// Root is the workspace root every path below resolves against.
	Root string

	// ServerDir is the root-relative dir holding one <service>/ dir per API server.
	ServerDir string

	// OpenAPISpec is a server's spec path under its <ServerDir>/<service>/ dir.
	OpenAPISpec string

	// PkgsiteHint is the install hint RequireTool shows when pkgsite is missing.
	PkgsiteHint string

	// GoPort is the port pkgsite listens on.
	GoPort string

	// PythonPort is the port pdoc listens on.
	PythonPort string

	// APIPort is the port the Redoc server listens on.
	APIPort string

	// PythonRoots are the root-relative dirs searched for Python packages.
	PythonRoots []string
}

// Deps are the server's collaborators.
type Deps struct {
	// Runner gates ServeGoDocs on pkgsite being installed; the other servers need none.
	Runner interfaces.CommandRunner

	// Supervisor starts the long-lived pkgsite and pdoc processes.
	Supervisor interfaces.Supervisor

	// Log reports start URLs and serve errors.
	Log interfaces.Logger

	// Keep keeps a discovered Python package (nil keeps every one).
	Keep func(relPath string) bool

	// Streams receive the pkgsite and pdoc processes' output.
	Streams types.Streams
}

// Server launches the local documentation servers. Each returns a running process or
// *http.Server the caller manages; Serve runs them until the context ends.
type Server struct {
	// d is the injected collaborators.
	d Deps

	// cfg is what the servers document and where they listen.
	cfg Config
}

// New returns a Server over cfg and d. It does no I/O.
func New(cfg Config, d Deps) *Server { return &Server{cfg: cfg, d: d} }

// Targets selects which servers Serve starts; none set starts all of them.
type Targets struct {
	// Go starts the Go pkgsite server.
	Go bool

	// Python starts the Python pdoc server.
	Python bool

	// API starts the Redoc OpenAPI server.
	API bool
}

// all reports whether no target is selected, meaning serve everything.
func (t Targets) all() bool { return !t.Go && !t.Python && !t.API }

// Serve starts the selected servers and blocks until ctx ends, then shuts them down.
// Go and Python start failures are fatal; an API failure is fatal only when API is the
// sole target, so a serve-all run still brings up the others. Starting nothing is not
// an error.
func (s *Server) Serve(ctx context.Context, targets Targets) error {
	if err := s.d.Streams.Require(); err != nil {
		return err
	}
	var procs []interfaces.ProcessHandle
	for _, start := range []struct {
		run func(context.Context) (interfaces.ProcessHandle, error)
		on  bool
	}{{s.ServeGoDocs, targets.Go}, {s.ServePythonDocs, targets.Python}} {
		if !start.on && !targets.all() {
			continue
		}
		p, err := start.run(ctx)
		if err != nil {
			return err
		}
		procs = append(procs, p)
	}
	var api *http.Server
	if targets.API || targets.all() {
		srv, err := s.ServeAPIDocs(ctx)
		switch {
		case err != nil && targets.API:
			return err
		case err != nil:
			s.d.Log.Warn("API docs server failed (continuing)", "error", err.Error())
		default:
			api = srv
		}
	}
	if len(procs) == 0 && api == nil {
		s.d.Log.Warn("No doc servers started")
		return nil
	}
	s.d.Log.Info("Press Ctrl+C to stop")
	<-ctx.Done()
	s.shutdown(ctx, api, procs)
	return nil
}

// shutdown stops api gracefully and waits for every process; a process stopped by the
// ended context exits non-zero, the expected shutdown, so exits are logged, not returned.
func (s *Server) shutdown(
	ctx context.Context, api *http.Server, procs []interfaces.ProcessHandle,
) {
	if api != nil {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		if err := api.Shutdown(stop); err != nil {
			s.d.Log.Warn("API docs server shutdown", "error", err.Error())
		}
		cancel()
	}
	for _, p := range procs {
		if err := p.Wait(); err != nil {
			s.d.Log.Debug("doc server exited", "error", err.Error())
		}
	}
}

// ServeGoDocs launches pkgsite on GoPort and returns the running process.
func (s *Server) ServeGoDocs(ctx context.Context) (interfaces.ProcessHandle, error) {
	if err := s.d.Runner.RequireTool("pkgsite", s.cfg.PkgsiteHint); err != nil {
		return nil, err
	}
	return s.start(ctx, "pkgsite", "Go", s.cfg.GoPort,
		[]string{"pkgsite", "-http=:" + s.cfg.GoPort})
}

// ServePythonDocs launches a live pdoc server on PythonPort over the Python packages
// under PythonRoots that Keep keeps. Each package is a standalone uv project, so pdoc
// runs through `uvx` and documents each by its src path; a package importing an
// unvendored dependency at import time may not render. None found is CodeInvalidInput.
func (s *Server) ServePythonDocs(ctx context.Context) (interfaces.ProcessHandle, error) {
	root := os.DirFS(s.cfg.Root)
	pkgs, err := python.Packages(root, s.cfg.PythonRoots, s.d.Keep)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "discover Python packages")
	}
	if len(pkgs) == 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"no documentable Python packages found",
		)
	}
	args := []string{
		"uvx", "pdoc", "--no-browser", "--host", "localhost", "--port",
		s.cfg.PythonPort,
	}
	for _, pkg := range pkgs {
		args = append(
			args,
			filepath.Join(s.cfg.Root, pkg, "src", python.ModuleName(root, pkg)),
		)
	}
	return s.start(ctx, "uvx", "Python", s.cfg.PythonPort, args)
}

// start runs args under the supervisor as name and logs what's URL on port.
func (s *Server) start(
	ctx context.Context, name, what, port string, args []string,
) (interfaces.ProcessHandle, error) {
	p, err := s.d.Supervisor.Start(ctx, types.ProcessSpec{
		Name: name, Args: args, Dir: s.cfg.Root,
		Stdout: s.d.Streams.Out, Stderr: s.d.Streams.Err,
	}, nil)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "start "+name)
	}
	s.d.Log.Info(what+" docs server started", "url", "http://localhost:"+port)
	return p, nil
}

// ServeAPIDocs starts a Redoc server on APIPort over every <ServerDir>/<service>/
// OpenAPISpec. One spec is served at / and /openapi.json; several each get /<service>
// and /<service>/openapi.json, under an index at /.
func (s *Server) ServeAPIDocs(ctx context.Context) (*http.Server, error) {
	specs, err := s.readSpecs()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	var root http.Handler
	if len(specs) == 1 {
		mux.Handle("/openapi.json", docs.SpecHandler(specs[0].data))
		root = docs.RedocHandler("/openapi.json", title(specs[0].name))
	} else {
		index := template.Must(template.New("index").Parse(indexPage))
		names := make([]string, 0, len(specs))
		for _, sp := range specs {
			url := "/" + sp.name + "/openapi.json"
			mux.Handle(url, docs.SpecHandler(sp.data))
			mux.Handle("/"+sp.name, docs.RedocHandler(url, title(sp.name)))
			names = append(names, sp.name)
		}
		root = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := index.Execute(w, names); err != nil {
				s.d.Log.Warn("render API docs index", "error", err.Error())
			}
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		root.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Addr:              ":" + s.cfg.APIPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "listen "+srv.Addr)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.d.Log.Warn("API docs server error", "error", err.Error())
		}
	}()
	s.d.Log.Info("API docs server started", "url", "http://localhost:"+s.cfg.APIPort)
	return srv, nil
}

// spec is one service's OpenAPI document.
type spec struct {
	// name is the service dir name.
	name string

	// data is the raw spec.
	data []byte
}

// readSpecs reads the spec of every service dir under ServerDir that has one: none is
// CodeNotFound, and a spec that exists but cannot be read is CodeInternal.
func (s *Server) readSpecs() ([]spec, error) {
	dir := filepath.Join(s.cfg.Root, filepath.FromSlash(s.cfg.ServerDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read "+dir)
	}
	var out []spec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name(), filepath.FromSlash(s.cfg.OpenAPISpec))
		data, err := os.ReadFile(
			p,
		)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "read "+p)
		}
		out = append(out, spec{name: e.Name(), data: data})
	}
	if len(out) == 0 {
		return nil, apperr.New(apperr.CodeNotFound,
			"no "+s.cfg.OpenAPISpec+" under "+dir+" (generate the specs first)")
	}
	return out, nil
}

// title capitalizes a service dir name for its page title.
func title(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
