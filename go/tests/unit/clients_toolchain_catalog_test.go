package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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

	tools := testClientsToolchainCatalogProjectsManifestByCategoryTable()

	cat, err := toolchain.NewCatalog(tools)
	require.NoError(t, err)

	require.Len(t, cat.Prerequisites(), 1)
	assert.Equal(t, textGoLower, cat.Prerequisites()[0].Binary)
	require.Len(t, cat.Required(), 1)
	assert.Equal(t, textBuf, cat.Required()[0].Binary)
	require.Len(t, cat.Optional(), 1)
	assert.Equal(t, textGrpcurl, cat.Optional()[0].Binary)

	// All() concatenates prereq + required + optional; ruff (registry-only) is out.
	require.Len(t, cat.All(), wantAllLen)

	// Find resolves a lifecycle spec across categories, with its projected method.
	buf, found := cat.Find(textBuf)
	require.True(t, found)
	assert.Equal(t, textGoLower, buf.InstallMethods[textAll].Command)

	_, found = cat.Find(textRuff)
	assert.False(t, found, "registry-only tool is not a lifecycle spec")

	_, found = cat.Find("nonexistent")
	assert.False(t, found)
}

// testClientsToolchainCatalogProjectsManifestByCategoryTable builds the table
// TestClientsToolchainCatalog_ProjectsManifestByCategory runs.
func testClientsToolchainCatalogProjectsManifestByCategoryTable() []types.ToolEntry {
	return []types.ToolEntry{
		fixtures.Build(func(row *types.ToolEntry) {
			row.Name = "Go"
			row.Binary = textGoLower
			row.Category = types.CategoryPrerequisite
			row.Required = true
			row.MinVersion = "1.26"
			row.VersionCmd = []string{textGoLower, textVersion}
			row.VersionParse = "go-version"
		}),
		fixtures.Build(func(row *types.ToolEntry) {
			row.Name = textBuf
			row.Binary = textBuf
			row.Category = types.CategoryRequired
			row.Required = true
			row.Install = map[string]types.InstallMethodEntry{
				textAll: fixtures.Build(func(row *types.InstallMethodEntry) {
					row.Kind = types.InstallKindGoInstall
					row.Package = "github.com/bufbuild/buf/cmd/buf@v1.69.0"
				}),
			}
		}),
		fixtures.Build(func(row *types.ToolEntry) {
			row.Name = textGrpcurl
			row.Binary = textGrpcurl
			row.Category = types.CategoryOptional
			row.Install = map[string]types.InstallMethodEntry{
				"darwin": fixtures.Build(func(row *types.InstallMethodEntry) {
					row.Kind = types.InstallKindCommand
					row.Command = "brew"
					row.Args = []string{textInstall, textGrpcurl}
				}),
			}
		}),
		// Registry-only (no category) — must be excluded from the catalog.
		fixtures.Build(func(row *types.ToolEntry) {
			row.Name = textRuff
			row.Binary = textRuff
			row.Cmd = []string{textUv, textRun, textRuff}
		}),
	}
}
