package codebase

import (
	"context"
	"go/parser"
	"go/token"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/sync/errgroup"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// goView is the Go view of a Codebase.
type goView struct {
	// parent supplies files and contents.
	parent *view

	// sets holds one FileSet per parse mode, shared by every file parsed in that mode.
	sets map[types.ParseMode]*token.FileSet
}

// Compile-time assertion that goView satisfies GoView.
var _ interfaces.GoView = (*goView)(nil)

// newGoView returns the Go view over r.
func newGoView(r *view) *goView {
	return &goView{parent: r, sets: map[types.ParseMode]*token.FileSet{
		types.ParseImports:  token.NewFileSet(),
		types.ParseComments: token.NewFileSet(),
	}}
}

// Modules returns the go.work `use` directories, else every go.mod's directory.
func (g *goView) Modules(ctx context.Context) ([]string, error) {
	return g.parent.snap.Memo(ctx, "go|modules", g.modules)
}

// modules computes Modules.
func (g *goView) modules(ctx context.Context) ([]string, error) {
	work, err := g.parent.Text(ctx, "go.work")
	switch {
	case err == nil:
		wf, perr := modfile.ParseWork("go.work", work.Bytes, nil)
		if perr != nil {
			return nil, apperr.Wrap(perr, apperr.CodeInvalidInput, "parse go.work")
		}
		out := make([]string, 0, len(wf.Use))
		for _, u := range wf.Use {
			out = append(out, path.Clean(u.Path))
		}
		slices.Sort(out)
		return out, nil
	case !apperr.Is(err, apperr.CodeNotFound):
		return nil, err
	}
	mods, err := g.parent.Files(ctx, types.FileSelector{Globs: []string{"**/go.mod"}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, path.Dir(m.Path))
	}
	return out, nil
}

// Parse parses the Go files sel selects, concurrently, each once per mode.
func (g *goView) Parse(
	ctx context.Context,
	sel types.FileSelector,
	mode types.ParseMode,
) ([]types.GoFile, error) {
	fset, ok := g.sets[mode]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput, "unknown parse mode")
	}
	files, err := g.parent.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	files = slices.DeleteFunc(files, func(f types.File) bool {
		return !strings.HasSuffix(f.Path, ".go")
	})
	out := make([]types.GoFile, len(files))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(runtime.NumCPU())
	for i, f := range files {
		eg.Go(func() error {
			parsed, err := g.parse(gctx, f.Path, mode, fset)
			out[i] = parsed
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// parse parses one file in mode, once per snapshot.
func (g *goView) parse(
	ctx context.Context,
	p string,
	mode types.ParseMode,
	fset *token.FileSet,
) (types.GoFile, error) {
	key := "go|parse|" + strconv.Itoa(int(mode)) + "|" + p
	return g.parent.snap.Memo(ctx, key, func(ctx context.Context) (types.GoFile, error) {
		text, err := g.parent.Text(ctx, p)
		if err != nil {
			return types.GoFile{}, err
		}
		flags := parser.ParseComments
		if mode == types.ParseImports {
			flags = parser.ImportsOnly | parser.ParseComments
		}
		file, err := parser.ParseFile(fset, p, text.Bytes, flags)
		if err != nil {
			return types.GoFile{}, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+p)
		}
		return types.GoFile{Path: p, FileSet: fset, AST: file}, nil
	})
}
