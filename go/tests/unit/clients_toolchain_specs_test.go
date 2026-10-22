package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestClientsToolchainProjectLifecycleTools_FromEmbeddedManifest verifies the manifest
// -> ToolDef projection over the real embedded toolchain.
//
// Why this test is important:
//   - Projection is the single bridge from config DATA to the install/detect
//     code; a wrong install-kind expansion or dropped field means setup tries
//     the wrong install command (or none) for a real tool.
//
// What it tests:
//   - A prerequisite (go) projects its min-version + version command and carries
//     no install methods.
//   - The command and go-install kinds expand to the correct concrete command;
//     the github-release and url kinds project to a pinned Download whose
//     per-platform artifact carries the full URL and SHA-256; `ci` projects.
//   - Descriptions project, and registry-only tools (ruff) are excluded.
func TestClientsToolchainProjectLifecycleTools_FromEmbeddedManifest(t *testing.T) {
	t.Parallel()

	cat, err := toolchain.NewCatalog(toolEntries(t))
	require.NoError(t, err)

	find := func(binary string) types.ToolDef {
		d, ok := cat.Find(binary)
		require.True(t, ok, "lifecycle tool %q must be projected", binary)
		return d
	}

	// Prerequisite: version detection, no install methods (manual only).
	goDef := find("go")
	require.NotNil(t, goDef.MinVersion)
	assert.Equal(t, "1.27", goDef.MinVersion.String())
	assert.Equal(t, []string{"go", "version"}, goDef.VersionCmd)
	assert.Empty(t, goDef.InstallMethods)

	// Required, command kind: uv's darwin method is a brew install.
	uv := find("uv")
	assert.Equal(
		t,
		types.InstallMethod{Command: "brew", Args: []string{"install", "uv"}},
		uv.InstallMethods["darwin"],
	)

	// Required, go-install kind: buf's "all" method expands to `go install`.
	buf := find("buf")
	assert.Equal(
		t, types.InstallMethod{
			Command: "go",
			Args:    []string{"install", manifestInstall(t, "buf", "all").Package},
		}, buf.InstallMethods["all"],
	)

	// Required, github-release kind: golangci-lint's windows method is a pinned
	// download of the release asset, verified against its SHA-256 (no command). The
	// expected artifact is derived from the manifest entry, so a pin bump needs no
	// test edit; the projection (org/repo/tag → release URL) is what is asserted.
	lintEntry := manifestInstall(t, "golangci-lint", "windows")
	lint := find("golangci-lint")
	win := lint.InstallMethods["windows"]
	assert.Empty(t, win.Command)
	require.NotNil(t, win.Download)
	assert.Equal(t, lintEntry.Binary, win.Download.Binary)
	assert.Equal(t, lintEntry.Version, win.Download.Version)
	asset := lintEntry.Assets["windows-amd64"]
	assert.Equal(t, types.Artifact{
		URL: "https://github.com/" + lintEntry.Org + "/" + lintEntry.Repo +
			"/releases/download/" + lintEntry.Tag + "/" + asset.Name,
		SHA256: asset.SHA256,
	}, win.Download.Artifacts["windows-amd64"])

	// CI tool, url kind: atlas downloads from its release host (asset appended to
	// the base URL), and the ci marker projects.
	atlasEntry := manifestInstall(t, "atlas", "all")
	atlas := find("atlas")
	assert.True(t, atlas.CI)
	require.NotNil(t, atlas.InstallMethods["all"].Download)
	assert.Equal(t, types.Artifact{
		URL:    atlasEntry.URL + "/" + atlasEntry.Assets["linux-amd64"].Name,
		SHA256: atlasEntry.Assets["linux-amd64"].SHA256,
	}, atlas.InstallMethods["all"].Download.Artifacts["linux-amd64"])
	assert.False(t, find("pgcli").CI, "a local-only tool is not in the CI set")

	// Optional: description projects from the toolchain.
	pgcli := find("pgcli")
	assert.Equal(t, "Enhanced PostgreSQL CLI", pgcli.Description)
	assert.Equal(
		t,
		types.InstallMethod{Command: "pip", Args: []string{"install", "pgcli"}},
		pgcli.InstallMethods["all"],
	)

	// Registry-only tools (no category) are not lifecycle specs.
	_, ok := cat.Find("ruff")
	assert.False(t, ok, "registry-only ruff must not be a lifecycle tool")

	// Sanity: every category is populated from the embedded toolchain.
	assert.NotEmpty(t, cat.Prerequisites())
	assert.NotEmpty(t, cat.Required())
	assert.NotEmpty(t, cat.Optional())
}

// TestClientsToolchainResolveVersionParse_Behavior pins the two declarative parse
// strategies.
//
// Why this test is important:
//   - The version-parse strategy is a string in YAML resolved to a Go func; the
//     golden test can't compare funcs, so the strategy's behavior must be pinned
//     here or a wrong mapping (e.g. go tools parsed as first-version) ships silently.
//
// What it tests:
//   - "go-version" extracts "1.26.0" from `go version go1.26.0 ...`.
//   - the default strategy extracts "0.5.1" from `uv 0.5.1`.
func TestClientsToolchainResolveVersionParse_Behavior(t *testing.T) {
	t.Parallel()

	cat, err := toolchain.NewCatalog(toolEntries(t))
	require.NoError(t, err)

	goDef, ok := cat.Find("go")
	require.True(t, ok)
	assert.Equal(t, "1.26.0", goDef.VersionParse("go version go1.26.0 darwin/arm64"))

	uvDef, ok := cat.Find("uv")
	require.True(t, ok)
	assert.Equal(t, "0.5.1", uvDef.VersionParse("uv 0.5.1"))
}

// manifestInstall returns the embedded manifest's install method for binary on
// platform, failing the test when either is missing.
func manifestInstall(t *testing.T, binary, platform string) toolchain.InstallMethodEntry {
	t.Helper()
	for i := range toolEntries(t) {
		if toolEntries(t)[i].Binary == binary {
			m, ok := toolEntries(t)[i].Install[platform]
			require.True(t, ok, "%s has no %s install method", binary, platform)
			return m
		}
	}
	require.Failf(t, "tool missing", "no %q tool in the manifest", binary)
	return toolchain.InstallMethodEntry{}
}
