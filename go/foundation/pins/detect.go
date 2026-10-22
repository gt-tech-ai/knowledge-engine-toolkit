// Package pins keeps a Renovate config in step with a codebase: it detects which
// dependency ecosystems the codebase holds (Detect), renders renovate.json from them
// and a consumer's update Policy (Render), and exposes the render as a Generator, so
// a drift check fails when an ecosystem appears and the config was not regenerated.
package pins

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Manager is a Renovate manager identifier (the value used in matchManagers).
type Manager string

// The Renovate managers Detect recognizes, one per ecosystem.
const (
	// ManagerGomod is the Go modules manager (go.mod, go.work).
	ManagerGomod Manager = "gomod"

	// ManagerPEP621 is the Python PEP 621 manager (pyproject.toml).
	ManagerPEP621 Manager = "pep621"

	// ManagerNpm is the npm/pnpm manager (package.json).
	ManagerNpm Manager = "npm"

	// ManagerDockerfile is the Dockerfile base-image manager.
	ManagerDockerfile Manager = "dockerfile"

	// ManagerDockerCompose is the docker-compose image manager.
	ManagerDockerCompose Manager = "docker-compose"

	// ManagerGithubActions is the GitHub Actions workflow manager.
	ManagerGithubActions Manager = "github-actions"

	// ManagerTerraform is the Terraform provider/module manager.
	ManagerTerraform Manager = "terraform"

	// ManagerTerragrunt is the Terragrunt manager.
	ManagerTerragrunt Manager = "terragrunt"

	// ManagerHelmv3 is the Helm v3 chart-dependency manager.
	ManagerHelmv3 Manager = "helmv3"
)

// EcosystemSet is what Detect found: the Renovate managers whose marker files are
// present, the ignore paths every config gets, and the root Go module path.
type EcosystemSet struct {
	// GoModule is the root go.mod's module path, the prefix every module of a Go
	// workspace shares; empty when there is no root go.mod.
	GoModule string

	// Managers are the managers present, sorted.
	Managers []Manager

	// IgnorePaths are the path globs Renovate skips, sorted.
	IgnorePaths []string
}

// Has reports whether m is present in the set.
func (e EcosystemSet) Has(m Manager) bool {
	return slices.Contains(e.Managers, m)
}

// HasAll reports whether every manager in ms is present; an empty ms is true.
func (e EcosystemSet) HasAll(ms []Manager) bool {
	for _, m := range ms {
		if !e.Has(m) {
			return false
		}
	}
	return true
}

// Detect reports the ecosystems cb holds from its marker files, skipping files under
// a .git, node_modules, .venv or vendor directory. A Chart.yaml under a charts/
// directory is a vendored subchart, so only a first-party chart enables helmv3.
func Detect(ctx context.Context, cb interfaces.Codebase) (EcosystemSet, error) {
	files, err := cb.Files(ctx, types.FileSelector{IncludeGenerated: true})
	if err != nil {
		return EcosystemSet{}, err
	}
	present, marks := map[Manager]bool{}, markers()
	for _, f := range files {
		if pruned(f.Path) {
			continue
		}
		if m, ok := classify(f.Path, marks); ok {
			present[m] = true
		}
	}
	managers := make([]Manager, 0, len(present))
	for m := range present {
		managers = append(managers, m)
	}
	slices.Sort(managers)

	var goModule string
	if text, err := cb.Text(ctx, "go.mod"); err == nil {
		goModule = modulePath(string(text.Bytes))
	}
	return EcosystemSet{
		Managers:    managers,
		IgnorePaths: []string{"**/node_modules/**", ".venv/**"},
		GoModule:    goModule,
	}, nil
}

// pruned reports whether p lies under a directory Detect skips.
func pruned(p string) bool {
	for seg := range strings.SplitSeq(path.Dir(p), "/") {
		switch seg {
		case ".git", "node_modules", ".venv", "vendor":
			return true
		}
	}
	return false
}

// markers maps a marker file's base name to the manager its presence implies.
func markers() map[string]Manager {
	return map[string]Manager{
		"go.mod":         ManagerGomod,
		"go.work":        ManagerGomod,
		"pyproject.toml": ManagerPEP621,
		"package.json":   ManagerNpm,
		"Dockerfile":     ManagerDockerfile,
		"terragrunt.hcl": ManagerTerragrunt,
	}
}

// classify returns the manager a file's presence implies, if any, given the marker
// names.
func classify(p string, marks map[string]Manager) (Manager, bool) {
	name := path.Base(p)
	if m, ok := marks[name]; ok {
		return m, true
	}
	lower := strings.ToLower(name)
	yml := strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
	switch {
	case yml && (strings.HasPrefix(lower, "docker-compose") ||
		strings.HasPrefix(lower, "compose")):
		return ManagerDockerCompose, true
	case strings.HasSuffix(name, ".tf"):
		return ManagerTerraform, true
	case name == "Chart.yaml":
		return ManagerHelmv3, !strings.Contains("/"+p, "/charts/")
	case yml && strings.HasPrefix(p, ".github/workflows/"),
		strings.HasPrefix(p, ".github/actions/") &&
			(name == "action.yml" || name == "action.yaml"):
		return ManagerGithubActions, true
	}
	return "", false
}

// modulePath returns the module directive's path in a go.mod, or "".
func modulePath(gomod string) string {
	for line := range strings.SplitSeq(gomod, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}
