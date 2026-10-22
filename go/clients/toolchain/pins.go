package toolchain

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// PathResolver resolves a tool's binary name to the executable found on PATH — the
// seam CheckPins reads the installed build through.
//
// SDK seam — wraps os/exec.LookPath; unit tests inject a generated mock.
type PathResolver interface {
	// LookPath returns the path of the named executable, or an error when it is not on
	// PATH.
	LookPath(file string) (string, error)
}

// execPathResolver is the production PathResolver (os/exec.LookPath).
type execPathResolver struct{}

// LookPath searches PATH for file.
func (execPathResolver) LookPath(
	file string,
) (string, error) {
	wrapRes0, wrapErr := exec.LookPath(file)
	return wrapRes0, apperr.Wrap(wrapErr, apperr.CodeInternal, "exec.LookPath")
}

// WithPathResolver overrides how CheckPins finds a tool's binary on PATH (default:
// os/exec.LookPath), so the pin check is testable without a binary on PATH.
func WithPathResolver(p PathResolver) Option {
	return func(i *Installer) { i.paths = p }
}

// WithReinstallHint overrides the command a pin drift tells the operator to run, given
// the tool's binary and its pinned package@version (default: `go install
// <package>@<version>`), so a front-end can point at its own install command.
func WithReinstallHint(hint func(binary, pkgAtVersion string) string) Option {
	return func(i *Installer) { i.reinstallHint = hint }
}

// CheckPins reports, one step per CI tool, whether the binary on PATH is the build the
// manifest pins. Only tools installed with `go install <package>@<version>` on this
// platform are checked: their binaries embed the package path and module version, which
// `go version -m` reads, so no per-tool version command is needed. A drift fails with the
// reinstall command; a tool that is not on PATH, or whose binary carries no build info,
// is skipped (the lint/format steps that need it report a missing tool themselves).
//
// This is the guard for formatter drift: gofumpt and golines at another version format
// the same file differently, so a local run that "passes" fails CI's format check.
func (i *Installer) CheckPins(
	ctx context.Context,
	root string,
	tools []types.ToolDef,
) types.StepResults {
	var out types.StepResults
	for j := range tools {
		tool := &tools[j]
		method, ok := i.resolveMethod(tool)
		if !ok || !isGoInstall(method) {
			continue
		}
		pkg, want, pinned := strings.Cut(method.Args[len(method.Args)-1], "@")
		if !pinned {
			continue
		}
		out = append(out, i.checkPin(ctx, root, tool, pkg, want))
	}
	return out
}

// checkPin compares one tool's binary on PATH with its pinned package and version.
func (i *Installer) checkPin(
	ctx context.Context,
	root string,
	tool *types.ToolDef,
	pkg, want string,
) types.StepResult {
	name := tool.Name + " is the pinned " + want
	path, err := i.paths.LookPath(tool.Binary)
	if err != nil {
		return types.StepResult{
			Name:   name,
			Status: types.StatusSkip,
			Error:  "not on PATH",
		}
	}
	res := i.runner.RunBuffered(ctx, root, "go", "version", "-m", path)
	gotPkg, got := buildInfo(string(res.Stdout))
	if res.Err != nil || got == "" {
		return types.StepResult{
			Name:   name,
			Status: types.StatusSkip,
			Error:  path + " carries no Go build info to check",
		}
	}
	if gotPkg == pkg && got == want {
		return types.StepResult{Name: name, Status: types.StatusPass}
	}
	fix := "go install " + pkg + "@" + want
	if i.reinstallHint != nil {
		fix = i.reinstallHint(tool.Binary, pkg+"@"+want)
	}
	return types.StepResult{
		Name:   name,
		Status: types.StatusFail,
		Error: fmt.Sprintf(
			"%s is %s@%s but the catalog pins %s@%s "+
				"(CI runs the pin, so its output differs): run %#q",
			path,
			gotPkg,
			got,
			pkg,
			want,
			fix,
		),
	}
}

// buildInfo extracts the main package path and its module version from `go version -m`
// output ("\tpath\t<pkg>" and "\tmod\t<module>\t<version>\t<sum>" lines); either is ""
// when absent.
func buildInfo(out string) (pkg, version string) {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 2 && fields[0] == "path":
			pkg = fields[1]
		case len(fields) >= 3 && fields[0] == "mod":
			version = fields[2]
		}
	}
	return pkg, version
}
