// Package mockgen renders the Go mocks the repository's `//go:generate mockgen`
// directives declare, as an interfaces.Generator: one artifact per directive, rendered
// by mockgen into memory, so a drift check (gen.Check) compares them with the on-disk
// mocks without writing to the tree.
package mockgen

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Name is the generator's name.
const Name = "mocks"

// Tool is the mockgen binary.
const Tool = "mockgen"

// InstallHint installs the pinned mockgen.
const InstallHint = "go install go.uber.org/mock/mockgen@v0.6.0"

// directivePrefix opens every go:generate line.
const directivePrefix = "//go:generate "

// destinationFlag is the mockgen flag naming the output file, relative to the
// directive's directory.
const destinationFlag = "-destination="

// NewGenerator returns the mocks Generator over dirs, the repository-relative mock
// directories holding `//go:generate mockgen` directives,
// running mockgen through runner under root.
func NewGenerator(
	dirs []string,
	runner interfaces.CommandRunner,
	root string,
) interfaces.Generator {
	return generator{dirs: dirs, runner: runner, root: root}
}

// generator renders the mocks of every directive under its dirs.
type generator struct {
	// runner runs mockgen.
	runner interfaces.CommandRunner

	// root is the repository root the dirs are relative to.
	root string

	// dirs are the repository-relative directories holding the directives.
	dirs []string
}

// Name implements interfaces.Generator.
func (generator) Name() string { return Name }

// Inputs selects every Go file: a mock derives from its directive and from the
// interface it mocks, which may live anywhere in the repository.
func (generator) Inputs() types.FileSelector {
	return types.FileSelector{Exts: []string{".go"}}
}

// Render runs each directive's mockgen with its output on stdout (no -destination), in
// the directive's directory as `go generate` would, and keys the rendered mock by the
// directive's destination. mockgen writes its argv into the file header, so the header
// line the stdout run writes is replaced by the one the directive's argv produces,
// making the artifact byte-equal to what `go generate` writes. A missing mockgen is
// CodeNotFound; a failing run is CodeInternal naming the mock and mockgen's stderr.
func (g generator) Render(
	ctx context.Context,
	cb interfaces.Codebase,
) (types.Artifacts, error) {
	if err := g.runner.RequireTool(Tool, InstallHint); err != nil {
		return nil, err
	}
	out := types.Artifacts{}
	for _, dir := range g.dirs {
		directives, err := directivesIn(ctx, cb, dir)
		if err != nil {
			return nil, err
		}
		for _, d := range directives {
			data, err := g.render(ctx, dir, d)
			if err != nil {
				return nil, err
			}
			out[path.Join(dir, d.dest)] = data
		}
	}
	return out, nil
}

// render runs one directive and returns the mock as `go generate` would write it.
func (g generator) render(ctx context.Context, dir string, d directive) ([]byte, error) {
	stdoutArgs := make([]string, 0, len(d.args))
	for _, a := range d.args {
		if !strings.HasPrefix(a, destinationFlag) {
			stdoutArgs = append(stdoutArgs, a)
		}
	}
	cwd := filepath.Join(g.root, filepath.FromSlash(dir))
	res := g.runner.RunBuffered(ctx, cwd, Tool, stdoutArgs...)
	if res.Err != nil {
		return nil, apperr.Wrap(res.Err, apperr.CodeInternal, fmt.Sprintf(
			"%s %s: %s",
			Tool,
			path.Join(dir, d.dest), // nosemgrep: use-filepath-join
			strings.TrimSpace(string(res.Stderr)),
		))
	}
	produced := []byte("//\t" + Tool + " " + strings.Join(stdoutArgs, " ") + "\n")
	wanted := []byte("//\t" + Tool + " " + strings.Join(d.args, " ") + "\n")
	return bytes.Replace(res.Stdout, produced, wanted, 1), nil
}

// directive is one parsed `//go:generate mockgen` line.
type directive struct {
	// dest is the -destination value, relative to the directive's directory.
	dest string

	// args is the directive's argv after the tool name.
	args []string
}

// directivesIn parses the go:generate lines of the Go files directly in dir, in file
// then line order, as `go generate` runs them. A directive that is not a plain mockgen
// argv (another tool, a quoted or $-expanded argument, no -destination) is
// CodeInvalidInput naming its line.
func directivesIn(
	ctx context.Context,
	cb interfaces.Codebase,
	dir string,
) ([]directive, error) {
	files, err := cb.Files(
		ctx,
		types.FileSelector{Roots: []string{dir}, Exts: []string{".go"}},
	)
	if err != nil {
		return nil, err
	}
	var out []directive
	for _, f := range files {
		if path.Dir(f.Path) != strings.TrimSuffix(dir, "/") {
			continue
		}
		text, err := cb.Text(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(text.Bytes), "\n") {
			if !strings.HasPrefix(line, directivePrefix) {
				continue
			}
			d, err := parseDirective(strings.TrimPrefix(line, directivePrefix))
			if err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
					fmt.Sprintf("%s:%d", f.Path, i+1))
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// parseDirective parses the argv after `//go:generate`.
func parseDirective(rest string) (directive, error) {
	args := strings.Fields(rest)
	if len(args) == 0 || args[0] != Tool {
		return directive{}, apperr.New(apperr.CodeInvalidInput,
			"the mocks generator renders only mockgen directives")
	}
	d := directive{args: args[1:]}
	for _, a := range d.args {
		if strings.ContainsAny(a, "\"'$`") {
			return directive{}, apperr.New(apperr.CodeInvalidInput,
				"quoted or $-expanded mockgen arguments are not supported")
		}
		if strings.HasPrefix(a, destinationFlag) {
			d.dest = strings.TrimPrefix(a, destinationFlag)
		}
	}
	if d.dest == "" {
		return directive{}, apperr.New(apperr.CodeInvalidInput,
			"a mockgen directive needs -destination=<file>")
	}
	return d, nil
}
