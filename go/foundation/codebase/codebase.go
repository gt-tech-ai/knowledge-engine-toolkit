// Package codebase builds a Codebase, a read-only view of one codebase, over a
// CodebaseSource: file selection, contents, pattern matches, the Go and YAML parsed
// forms and rendered charts. Every view is memoized on a Snapshot, so concurrent checks
// share one listing, one read and one parse of each file and one render of each chart
// for a run.
package codebase

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// view is the Codebase built over a source.
type view struct {
	// src lists and reads the repository's files.
	src interfaces.CodebaseSource

	// renderer renders charts; nil makes Rendered CodeUnavailable.
	renderer interfaces.Renderer

	// snap memoizes every view.
	snap *Snapshot

	// goView is the Go view over this Codebase.
	goView *goView

	// yamlView is the YAML view over this Codebase.
	yamlView *yamlView
}

// Compile-time assertion that view satisfies Codebase.
var _ interfaces.Codebase = (*view)(nil)

// New returns the Codebase over src, memoized on snap; renderer may be nil.
func New(
	src interfaces.CodebaseSource,
	snap *Snapshot,
	renderer interfaces.Renderer,
) interfaces.Codebase {
	v := &view{src: src, snap: snap, renderer: renderer}
	v.goView = newGoView(v)
	v.yamlView = &yamlView{parent: v}
	return v
}

// Files returns the files sel selects, sorted by path.
func (v *view) Files(ctx context.Context, sel types.FileSelector) ([]types.File, error) {
	all, err := v.snap.Memo(ctx, "list", v.src.List)
	if err != nil {
		return nil, err
	}
	var out []types.File
	for _, f := range all {
		if !Matches(sel, f.Path) || (sel.TrackedOnly && f.Untracked) {
			continue
		}
		if !sel.IncludeGenerated && strings.HasSuffix(f.Path, ".go") {
			generated, err := v.generated(ctx, f.Path)
			if err != nil {
				return nil, err
			}
			if generated {
				continue
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// generated reports whether the Go file at p is generated, by name or by header.
func (v *view) generated(ctx context.Context, p string) (bool, error) {
	if GeneratedName(p) {
		return true, nil
	}
	text, err := v.Text(ctx, p)
	if err != nil {
		return false, err
	}
	return GeneratedHeader(text.Bytes), nil
}

// Text returns one file's contents, read once per snapshot.
func (v *view) Text(ctx context.Context, p string) (types.Text, error) {
	if err := checkPath(p); err != nil {
		return types.Text{}, err
	}
	return v.snap.Memo(ctx, "text|"+p, func(ctx context.Context) (types.Text, error) {
		data, err := v.src.Read(ctx, p)
		if err != nil {
			return types.Text{}, err
		}
		return types.Text{Path: p, Bytes: data, Binary: types.IsBinary(data)}, nil
	})
}

// checkPath rejects a path that is absolute or climbs out of the repository.
func checkPath(p string) error {
	clean := path.Clean(p)
	if p == "" || path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") {
		return apperr.New(apperr.CodeInvalidInput, "path outside the repository: "+p)
	}
	return nil
}

// Match scans every text file sel selects, concurrently, and returns the lines that
// match a pattern, in file then line order.
func (v *view) Match(
	ctx context.Context,
	sel types.FileSelector,
	patterns []*regexp.Regexp,
) ([]types.Hit, error) {
	files, err := v.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	perFile := make([][]types.Hit, len(files))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i, f := range files {
		g.Go(func() error {
			text, err := v.Text(gctx, f.Path)
			if err != nil {
				return err
			}
			if !text.Binary {
				perFile[i] = scan(text, patterns)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(perFile...), nil
}

// scan returns text's lines that match one of patterns, the first match per line.
func scan(text types.Text, patterns []*regexp.Regexp) []types.Hit {
	var out []types.Hit
	for i, line := range text.Lines() {
		for _, re := range patterns {
			if loc := re.FindStringIndex(line); loc != nil {
				out = append(out, types.Hit{
					File: text.Path, Line: i + 1, Col: loc[0] + 1,
					Text: line, Pattern: re.String(),
				})
				break
			}
		}
	}
	return out
}

// Changed returns the paths that differ from base plus the untracked files, once per
// base.
func (v *view) Changed(ctx context.Context, base string) (map[string]bool, error) {
	return v.snap.Memo(
		ctx,
		"changed|"+base,
		func(ctx context.Context) (map[string]bool, error) {
			return v.src.Changed(ctx, base)
		},
	)
}

// Go returns the Go view.
func (v *view) Go() interfaces.GoView { return v.goView }

// YAML returns the YAML view.
func (v *view) YAML() interfaces.YAMLView { return v.yamlView }

// Rendered renders spec once per snapshot.
func (v *view) Rendered(
	ctx context.Context,
	spec types.RenderSpec,
) ([]types.Object, error) {
	if v.renderer == nil {
		return nil, apperr.New(apperr.CodeUnavailable, "no renderer configured")
	}
	key, err := json.Marshal(spec)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "render spec")
	}
	return v.snap.Memo(ctx, "render|"+string(key),
		func(ctx context.Context) ([]types.Object, error) {
			return v.renderer.Render(ctx, spec)
		})
}
