package golang

import (
	"context"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
)

// IsRaceSupported reports whether the Go race detector is available on the
// current OS/architecture. Notably, windows/arm64 does not support -race.
func IsRaceSupported() bool {
	return runtime.GOOS != "windows" || runtime.GOARCH != "arm64"
}

// goListResult memoizes one directory's `go list ./...` outcome.
type goListResult struct {
	// err is the error from the memoized `go list ./...`, if it failed.
	err error
	// pkgs is the list of package import paths returned by `go list ./...`.
	pkgs []string
}

// Lister runs and memoizes `go list ./...` per directory for the lifetime of
// one run. Build one per run, never per process: a long-lived server that kept
// a Lister would serve a stale listing after the tree changed. Within a run one
// Lister is shared across every phase (format, lint, typecheck, security, test),
// so the repeated `go list` calls collapse to one listing per directory. The
// zero value is not usable; call NewLister.
type Lister struct {
	// once ensures each dir's listing runs exactly once per Lister.
	once map[string]*sync.Once

	// cache holds each dir's completed listing for this Lister.
	cache map[string]goListResult
	// fsys is the repository, rooted at root; a failed listing reads it to tell a
	// broken module from a directory with no go.mod.
	fsys fs.FS

	// root is the repository root the listed directories are absolute paths under.
	root string

	// mu guards the two memo maps (held only for map access, never across the
	// subprocess, so distinct dirs still list concurrently).
	mu sync.Mutex
}

// NewLister returns a Lister with empty memo maps, scoped to one run, over
// the repository fsys rooted at root.
func NewLister(fsys fs.FS, root string) *Lister {
	return &Lister{
		once:  map[string]*sync.Once{},
		cache: map[string]goListResult{},
		fsys:  fsys,
		root:  root,
	}
}

// isGoModule reports whether dir is a Go module root (contains a go.mod). It
// disambiguates a `go list` failure: a load error inside a real module means the
// module's code is broken (keep it so the tool fails loudly), whereas a non-module
// directory is simply not ours to test (drop it). dir is an absolute path under the
// Lister's root, read through its fs.FS.
func (l *Lister) isGoModule(dir string) bool {
	rel, err := filepath.Rel(l.root, dir)
	if err != nil || l.fsys == nil {
		return false
	}
	_, err = fs.Stat(l.fsys, filepath.ToSlash(filepath.Join(rel, "go.mod")))
	return err == nil
}

// GoListPackages returns the Go package import paths under dir (`go list
// ./...`, run through r), memoized per directory for this Lister's lifetime
// (one run). The workspace is stable within a run, so caching collapses the
// repeated module-eligibility probes of every phase to a single `go list` per
// directory. The memo is keyed by directory alone, so the first caller's
// runner serves every later call for that dir. Concurrent calls for the same
// dir dedupe on a per-dir sync.Once; distinct dirs list in parallel. A failed
// listing is cached too, as a CodeInternal error carrying go's stderr.
func (l *Lister) GoListPackages(
	ctx context.Context,
	r interfaces.CommandRunner,
	dir string,
) ([]string, error) {
	l.mu.Lock()
	once, ok := l.once[dir]
	if !ok {
		once = &sync.Once{}
		l.once[dir] = once
	}
	l.mu.Unlock()

	once.Do(func() {
		pkgs, err := goList(ctx, r, dir)
		l.mu.Lock()
		l.cache[dir] = goListResult{pkgs: pkgs, err: err}
		l.mu.Unlock()
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	res := l.cache[dir]
	return res.pkgs, res.err
}

// goList runs `go list [flags] ./...` in dir through r and returns the package
// import paths (whitespace-split stdout), or a CodeInternal error carrying
// go's stderr.
func goList(
	ctx context.Context,
	r interfaces.CommandRunner,
	dir string,
	flags ...string,
) ([]string, error) {
	args := append(append([]string{"list"}, flags...), "./...")
	res := r.RunBuffered(ctx, dir, "go", args...)
	if res.Err != nil {
		return nil, apperr.Wrap(res.Err, apperr.CodeInternal,
			"go list in "+dir+": "+strings.TrimSpace(string(res.Stderr)))
	}
	return strings.Fields(string(res.Stdout)), nil
}

// hasGoPackages reports whether the module at dir contains at least one Go
// package loadable under default build constraints. It reads the memoized
// GoListPackages. A `go list` error inside a real module (has go.mod) means
// the module's Go code fails to load — a package-name conflict or build error
// — NOT that it has no packages, so it reports the module PRESENT: the module
// stays in the discovery set and the downstream tool (go test / vet / lint)
// runs and fails loudly on the real error; dropping it would let a
// non-compiling package pass a gate unseen. A non-module dir is not ours to
// test.
func (l *Lister) hasGoPackages(
	ctx context.Context,
	r interfaces.CommandRunner,
	dir string,
) bool {
	pkgs, err := l.GoListPackages(ctx, r, dir)
	if err != nil {
		return l.isGoModule(dir)
	}
	return len(pkgs) > 0
}

// HasGoPackagesWithTags reports whether the module at dir contains at least
// one Go package loadable under the given build tags (`go list -tags=<tags>
// ./...` through r, not memoized — the tagged listing is distinct from the
// default-constraint memo). Like hasGoPackages, a load failure inside a real
// module reports it present, so the tagged build fails loudly rather than
// silently dropping a build break; a non-module dir reports false.
func (l *Lister) HasGoPackagesWithTags(
	ctx context.Context,
	r interfaces.CommandRunner,
	dir, tags string,
) bool {
	pkgs, err := goList(ctx, r, dir, "-tags="+tags)
	if err != nil {
		return l.isGoModule(dir)
	}
	return len(pkgs) > 0
}

// FilterModulesWithGoPackages returns the modules (relative to root) that
// contain loadable Go packages, in their original order. The per-module probes
// (`go list` through r) fan out through engine.Pool.FanOut at NumCPU and share this
// Lister's memo, so a module probed here is not listed again when a later
// phase reuses the Lister.
func (l *Lister) FilterModulesWithGoPackages(
	ctx context.Context,
	r interfaces.CommandRunner,
	root string,
	modules []string,
) []string {
	type probe struct {
		// dir is the module's absolute directory.
		dir string

		// i is the module's index, so each probe fills its own keep slot.
		i int
	}
	probes := make([]probe, len(modules))
	for i, mod := range modules {
		probes[i] = probe{dir: filepath.Join(root, mod), i: i}
	}
	keep := make([]bool, len(modules))
	engine.Pool(0).
		FanOut(ctx, probes, func(ctx context.Context, p probe) types.StepResult {
			keep[p.i] = l.hasGoPackages(ctx, r, p.dir)
			return types.StepResult{Name: p.dir, Status: types.StatusPass}
		})

	filtered := make([]string, 0, len(modules))
	for i, mod := range modules {
		if keep[i] {
			filtered = append(filtered, mod)
		}
	}
	return filtered
}
