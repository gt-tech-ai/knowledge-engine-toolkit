package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
)

// TestClientsToolchainCatalog_ProjectsManifestByCategory verifies the injected spec
// provider.
//
// Why this test is important:
//   - The Catalog is the DI seam the composition root hands to detect/install;
//     if it mis-buckets a tool by category (or includes a registry-only tool),
//     setup checks the wrong set or a tool listing lists a non-tool.
//
// What it tests:
//   - Prerequisites/Required/Optional each return only their category.
//   - All() concatenates the three and excludes registry-only entries.
//   - Find resolves a lifecycle spec across categories and reports false for
//     registry-only or unknown binaries.
func TestClientsToolchainCatalog_ProjectsManifestByCategory(t *testing.T) {
	t.Parallel()

	tools := []toolchain.ToolEntry{
		{
			Name: "Go", Binary: "go", Category: toolchain.CategoryPrerequisite,
			Required: true, MinVersion: "1.26",
			VersionCmd: []string{"go", "version"}, VersionParse: "go-version",
		},
		{
			Name: "buf", Binary: "buf", Category: toolchain.CategoryRequired,
			Required: true,
			Install: map[string]toolchain.InstallMethodEntry{
				"all": {
					Kind:    toolchain.InstallKindGoInstall,
					Package: "github.com/bufbuild/buf/cmd/buf@v1.69.0",
				},
			},
		},
		{
			Name: "grpcurl", Binary: "grpcurl", Category: toolchain.CategoryOptional,
			Install: map[string]toolchain.InstallMethodEntry{
				"darwin": {
					Kind: toolchain.InstallKindCommand, Command: "brew",
					Args: []string{"install", "grpcurl"},
				},
			},
		},
		// Registry-only (no category) — must be excluded from the catalog.
		{Name: "ruff", Binary: "ruff", Cmd: []string{"uv", "run", "ruff"}},
	}

	cat, err := toolchain.NewCatalog(tools)
	require.NoError(t, err)

	require.Len(t, cat.Prerequisites(), 1)
	assert.Equal(t, "go", cat.Prerequisites()[0].Binary)
	require.Len(t, cat.Required(), 1)
	assert.Equal(t, "buf", cat.Required()[0].Binary)
	require.Len(t, cat.Optional(), 1)
	assert.Equal(t, "grpcurl", cat.Optional()[0].Binary)

	// All() concatenates prereq + required + optional; ruff (registry-only) is out.
	require.Len(t, cat.All(), 3)

	// Find resolves a lifecycle spec across categories, with its projected method.
	buf, ok := cat.Find("buf")
	require.True(t, ok)
	assert.Equal(t, "go", buf.InstallMethods["all"].Command)

	_, ok = cat.Find("ruff")
	assert.False(t, ok, "registry-only tool is not a lifecycle spec")
	_, ok = cat.Find("nonexistent")
	assert.False(t, ok)
}
