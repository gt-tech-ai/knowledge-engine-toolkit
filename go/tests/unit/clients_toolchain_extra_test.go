package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ---------------------------------------------------------------------------
// DefsFromTools
// ---------------------------------------------------------------------------

// TestClientsToolchainDefsFromTools_ProjectsTools tests that DefsFromTools correctly
// maps a manifest's tools section into Def descriptors.
//
// Why this test is important:
//   - DefsFromTools is the bridge between the loaded YAML manifest and the
//     in-memory tool registry used by all CLI commands
//   - An incorrect mapping silently breaks tool discovery and command construction
//
// What it tests:
//   - Each manifest tool entry produces a matching Def with Name, Binary, InstallHint
//   - The Prefix (Cmd) field is forwarded correctly
//   - An empty manifest produces an empty Def slice
func TestClientsToolchainDefsFromTools_ProjectsTools(t *testing.T) {
	t.Parallel()

	defs := toolchain.DefsFromTools([]types.ToolEntry{
		{
			Name:        textGolangciLint,
			Binary:      textGolangciLint,
			InstallHint: "brew install golangci-lint",
		},
		{Name: textRuff, Binary: textRuff, Cmd: []string{textUv, textRun}},
	})
	require.Len(t, defs, wantDefsLen2, "DefsFromTools should return 2 defs")

	assert.Equal(t, textGolangciLint, defs[0].Name)
	assert.Equal(t, textGolangciLint, defs[0].Binary)
	assert.Equal(t, "brew install golangci-lint", defs[0].InstallHint)

	assert.Equal(t, textRuff, defs[1].Name)
	require.Len(t, defs[1].Prefix, wantPrefixLen2, "expected 2-element prefix for ruff")
	assert.Equal(t, textUv, defs[1].Prefix[0])
}

// TestClientsToolchainDefsFromTools_EmptyTools tests that an empty tools section
// produces no defs.
//
// Why this test is important:
//   - A manifest with no tools is valid; building a registry from it must not panic
//
// What it tests:
//   - Empty Tools slice yields empty Def slice (not nil panic)
func TestClientsToolchainDefsFromTools_EmptyTools(t *testing.T) {
	t.Parallel()

	defs := toolchain.DefsFromTools(nil)
	assert.Empty(t, defs, "expected 0 defs from empty manifest")
}

// ---------------------------------------------------------------------------
// Registry - concurrent safety
// ---------------------------------------------------------------------------

// TestClientsToolchainRegistry_LenAfterMultipleRegistrations tests that Len accurately
// reflects the number of registered tools, including overwrite of duplicate binary names.
//
// Why this test is important:
//   - Len is used by tests and health checks to verify registry population
//   - Re-registering a tool with the same binary name must not inflate the count
//
// What it tests:
//   - Registering two tools with distinct binaries yields Len == 2
//   - Registering a tool with the same binary as an existing entry keeps Len == 2
func TestClientsToolchainRegistry_LenAfterMultipleRegistrations(t *testing.T) {
	t.Parallel()

	reg := toolchain.NewRegistryFromDefs([]toolchain.Def{
		{Name: textBuf, Binary: textBuf},
		{Name: textRuff, Binary: textRuff},
	})
	assert.Equal(t, wantLen2, reg.Len(), "Len() after 2 distinct registrations")

	// Re-register "buf" - should overwrite, not add.
	reg.Register(toolchain.NewTool("buf-v2", textBuf, "", nil))
	assert.Equal(t, wantLen2, reg.Len(), "Len() after overwrite should remain 2")
}
