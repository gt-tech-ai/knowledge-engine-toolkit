package toolchain

import (
	"fmt"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ProjectLifecycleTools projects the lifecycle tools (those carrying a category)
// of the manifest's tools section into types.ToolDef values. Registry-only
// entries (no category) are skipped — they exist solely for RequireTool via
// DefsFromTools.
func ProjectLifecycleTools(tools []types.ToolEntry) ([]types.ToolDef, error) {
	var out []types.ToolDef

	for i := range tools {
		entry := tools[i]
		if !entry.InLifecycle() {
			continue
		}

		def, err := projectTool(&entry)
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
	tools []types.ToolEntry,
	cat types.ToolCategory,
) ([]types.ToolDef, error) {
	var out []types.ToolDef

	for i := range tools {
		entry := tools[i]
		if entry.Category != cat {
			continue
		}

		def, err := projectTool(&entry)
		if err != nil {
			return nil, err
		}

		out = append(out, def)
	}

	return out, nil
}

// projectTool converts a single manifest types.ToolEntry into a types.ToolDef,
// resolving the declarative version-parse strategy to a parser and each
// per-platform install method to a concrete command.
func projectTool(toolEntry *types.ToolEntry) (types.ToolDef, error) {
	def := types.ToolDef{
		Name:           toolEntry.Name,
		Binary:         toolEntry.Binary,
		Description:    toolEntry.Description,
		VersionCmd:     toolEntry.VersionCmd,
		VersionParse:   resolveVersionParse(toolEntry.VersionParse),
		Required:       toolEntry.Required,
		FallbackURL:    toolEntry.FallbackURL,
		CI:             toolEntry.CI,
		MinVersion:     nil,
		InstallMethods: nil,
	}

	if toolEntry.MinVersion != "" {
		version, err := types.ParseVersion(toolEntry.MinVersion)
		if err != nil {
			return types.ToolDef{}, apperr.Wrap(
				err, apperr.CodeInvalidInput,
				fmt.Sprintf("tool %q min_version", toolEntry.Binary),
			)
		}

		def.MinVersion = &version
	}

	if len(toolEntry.Install) > 0 {
		methods := make(map[string]types.InstallMethod, len(toolEntry.Install))
		for platform := range toolEntry.Install {
			methods[platform] = buildInstallMethod(new(toolEntry.Install[platform]))
		}

		def.InstallMethods = methods
	}

	return def, nil
}

// resolveVersionParse maps a declarative strategy name to its parser. The empty
// string and "first-version" both yield the default first-digits parser.
func resolveVersionParse(strategy string) func(string) string {
	switch strategy {
	case types.VersionParseGo:
		return parseGoVersion
	default:
		return types.ParseFirstVersion
	}
}

// parseGoVersion extracts the version from `go version go1.26.0 darwin/arm64`.
func parseGoVersion(text string) string {
	for part := range strings.FieldsSeq(text) {
		if len(part) > minGoPartLen && part[0] == 'g' && part[1] == 'o' &&
			part[goVersionDigitIndex] >= '0' &&
			part[goVersionDigitIndex] <= '9' {
			return part[goPrefixLen:]
		}
	}

	return types.ParseFirstVersion(text)
}

// buildInstallMethod turns a declarative install method into a concrete one.
// This is the install *mechanism* the params-only manifest does not encode:
// go-install expands to `go install`, and the two download kinds become a pinned
// Download the Installer fetches, verifies, and places itself.
func buildInstallMethod(
	installMethodEntry *types.InstallMethodEntry,
) types.InstallMethod {
	switch installMethodEntry.Kind {
	case types.InstallKindGoInstall:
		return types.InstallMethod{
			Command:  "go",
			Args:     []string{"install", installMethodEntry.Package},
			Download: nil,
		}
	case types.InstallKindGitHubRelease:
		base := fmt.Sprintf(
			"https://github.com/%s/%s/releases/download/%s",
			installMethodEntry.Org,
			installMethodEntry.Repo,
			installMethodEntry.Tag,
		)

		return types.InstallMethod{
			Download: buildDownload(installMethodEntry, base),
			Command:  "",
			Args:     nil,
		}
	case types.InstallKindURL:
		return types.InstallMethod{
			Download: buildDownload(
				installMethodEntry,
				strings.TrimSuffix(installMethodEntry.URL, "/"),
			),
			Command: "",
			Args:    nil,
		}
	case types.InstallKindCommand:
		return types.InstallMethod{
			Command:  installMethodEntry.Command,
			Args:     installMethodEntry.Args,
			Download: nil,
		}
	default:
		// Validation rejects any other kind, so this result is unreachable from config.
	}

	return types.InstallMethod{
		Command:  installMethodEntry.Command,
		Args:     installMethodEntry.Args,
		Download: nil,
	}
}

// buildDownload projects a download kind's per-platform assets into artifacts
// whose URL is base + "/" + the asset name.
func buildDownload(
	installMethodEntry *types.InstallMethodEntry,
	base string,
) *types.Download {
	artifacts := make(map[string]types.Artifact, len(installMethodEntry.Assets))
	for platform, a := range installMethodEntry.Assets {
		artifacts[platform] = types.Artifact{
			URL:    base + "/" + a.Name,
			SHA256: a.SHA256,
			Member: a.Member,
		}
	}

	return &types.Download{
		Artifacts: artifacts,
		Binary:    installMethodEntry.Binary,
		Version:   installMethodEntry.Version,
	}
}
