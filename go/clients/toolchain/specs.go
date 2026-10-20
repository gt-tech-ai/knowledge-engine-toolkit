package toolchain

import (
	"fmt"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ProjectLifecycleTools projects the lifecycle tools (those carrying a category)
// of the manifest's tools section into types.ToolDef values. Registry-only
// entries (no category) are skipped — they exist solely for RequireTool via
// DefsFromTools.
func ProjectLifecycleTools(tools []ToolEntry) ([]types.ToolDef, error) {
	var out []types.ToolDef
	for i := range tools {
		entry := tools[i]
		if !entry.InLifecycle() {
			continue
		}
		def, err := projectTool(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, nil
}

// ProjectToolsByCategory returns the lifecycle tools of one category from the
// manifest's tools section, in manifest order. Install ordering matters (e.g.
// Node before pnpm), so the slice preserves the manifest sequence.
func ProjectToolsByCategory(
	tools []ToolEntry,
	cat ToolCategory,
) ([]types.ToolDef, error) {
	var out []types.ToolDef
	for i := range tools {
		entry := tools[i]
		if entry.Category != cat {
			continue
		}
		def, err := projectTool(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, nil
}

// projectTool converts a single manifest ToolEntry into a types.ToolDef,
// resolving the declarative version-parse strategy to a parser and each
// per-platform install method to a concrete command.
func projectTool(e ToolEntry) (types.ToolDef, error) {
	def := types.ToolDef{
		Name:         e.Name,
		Binary:       e.Binary,
		Description:  e.Description,
		VersionCmd:   e.VersionCmd,
		VersionParse: resolveVersionParse(e.VersionParse),
		Required:     e.Required,
		FallbackURL:  e.FallbackURL,
		CI:           e.CI,
	}

	if e.MinVersion != "" {
		v, err := types.ParseVersion(e.MinVersion)
		if err != nil {
			return types.ToolDef{}, apperr.Wrap(
				err, apperr.CodeInvalidInput,
				fmt.Sprintf("tool %q min_version", e.Binary),
			)
		}
		def.MinVersion = &v
	}

	if len(e.Install) > 0 {
		methods := make(map[string]types.InstallMethod, len(e.Install))
		for platform := range e.Install {
			methods[platform] = buildInstallMethod(e.Install[platform])
		}
		def.InstallMethods = methods
	}

	return def, nil
}

// resolveVersionParse maps a declarative strategy name to its parser. The empty
// string and "first-version" both yield the default first-digits parser.
func resolveVersionParse(strategy string) func(string) string {
	switch strategy {
	case VersionParseGo:
		return parseGoVersion
	default:
		return types.ParseFirstVersion
	}
}

// parseGoVersion extracts the version from `go version go1.26.0 darwin/arm64`.
func parseGoVersion(s string) string {
	for part := range strings.FieldsSeq(s) {
		if len(part) > 2 && part[0] == 'g' && part[1] == 'o' && part[2] >= '0' &&
			part[2] <= '9' {
			return part[2:]
		}
	}
	return types.ParseFirstVersion(s)
}

// buildInstallMethod turns a declarative install method into a concrete one.
// This is the install *mechanism* the params-only manifest does not encode:
// go-install expands to `go install`, and the two download kinds become a pinned
// Download the Installer fetches, verifies, and places itself.
func buildInstallMethod(m InstallMethodEntry) types.InstallMethod {
	switch m.Kind {
	case InstallKindGoInstall:
		return types.InstallMethod{Command: "go", Args: []string{"install", m.Package}}
	case InstallKindGitHubRelease:
		base := fmt.Sprintf(
			"https://github.com/%s/%s/releases/download/%s",
			m.Org,
			m.Repo,
			m.Tag,
		)
		return types.InstallMethod{Download: buildDownload(m, base)}
	case InstallKindURL:
		return types.InstallMethod{
			Download: buildDownload(m, strings.TrimSuffix(m.URL, "/")),
		}
	case InstallKindCommand:
	}
	// InstallKindCommand; validation rejects any other kind.
	return types.InstallMethod{Command: m.Command, Args: m.Args}
}

// buildDownload projects a download kind's per-platform assets into artifacts
// whose URL is base + "/" + the asset name.
func buildDownload(m InstallMethodEntry, base string) *types.Download {
	artifacts := make(map[string]types.Artifact, len(m.Assets))
	for platform, a := range m.Assets {
		artifacts[platform] = types.Artifact{
			URL:    base + "/" + a.Name,
			SHA256: a.SHA256,
		}
	}
	return &types.Download{Artifacts: artifacts, Binary: m.Binary, Version: m.Version}
}
