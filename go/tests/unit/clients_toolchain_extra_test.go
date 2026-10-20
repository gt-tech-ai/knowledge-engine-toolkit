package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
)

// ---------------------------------------------------------------------------
// Profile arg helpers - uncovered functions
// ---------------------------------------------------------------------------

// TestClientsToolchainGofumptArgs tests that GofumptArgs returns the -l -w flags for a
// path.
//
// Why this test is important:
//   - gofumpt must always receive -l (list changed files) and -w (write in place)
//   - The path argument must be forwarded as the third element
//
// What it tests:
//   - Returns exactly [-l, -w, <path>]
func TestClientsToolchainGofumptArgs(t *testing.T) {
	t.Parallel()
	args := toolchain.GofumptArgs("/some/path")
	require.Len(t, args, 3, "GofumptArgs should return exactly 3 args")
	assert.Equal(t, "-l", args[0])
	assert.Equal(t, "-w", args[1])
	assert.Equal(t, "/some/path", args[2])
}

// TestClientsToolchainRuffFormatArgs_WithCheck tests that RuffFormatArgs includes
// --check in check mode.
//
// Why this test is important:
//   - The CI format step runs ruff format --check to detect unformatted files
//   - Missing --check would silently reformat files instead of reporting failures
//
// What it tests:
//   - check=true appends --check to the args
//   - check=false does not include --check
func TestClientsToolchainRuffFormatArgs_WithCheck(t *testing.T) {
	t.Parallel()
	withCheck := toolchain.RuffFormatArgs(true)
	assert.Contains(t, withCheck, "--check", "expected --check in RuffFormatArgs(true)")

	withoutCheck := toolchain.RuffFormatArgs(false)
	assert.NotContains(
		t,
		withoutCheck,
		"--check",
		"unexpected --check in RuffFormatArgs(false)",
	)
}

// TestClientsToolchainBasedpyrightArgs tests that BasedpyrightArgs builds the correct uv
// run invocation for a given directory.
//
// Why this test is important:
//   - basedpyright runs via uv run to pick up the project's virtual environment
//   - The directory argument must be the last element so basedpyright scans the
//     correct package root
//
// What it tests:
//   - Returns [run, basedpyright, <dir>]
//   - The first arg is "run" (uv sub-command)
func TestClientsToolchainBasedpyrightArgs(t *testing.T) {
	t.Parallel()
	dir := "/pkg/python/techai_webutils"
	args := toolchain.BasedpyrightArgs(dir)
	require.GreaterOrEqual(t, len(args), 3, "BasedpyrightArgs should return >= 3 args")
	assert.Equal(t, "run", args[0])
	assert.Equal(t, "basedpyright", args[1])
	assert.Equal(t, dir, args[len(args)-1])
}

// TestClientsToolchainGoVetArgs tests that GoVetArgs returns the correct go vet
// invocation.
//
// Why this test is important:
//   - go vet must always target ./... to cover all packages in the module
//   - An empty or wrong arg list would miss packages or fail to run
//
// What it tests:
//   - GoVetArgs returns [vet, ./...]
func TestClientsToolchainGoVetArgs(t *testing.T) {
	t.Parallel()
	args := toolchain.GoVetArgs()
	require.Len(t, args, 2, "GoVetArgs should return exactly 2 args")
	assert.Equal(t, "vet", args[0])
	assert.Equal(t, "./...", args[1])
}

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
	defs := toolchain.DefsFromTools([]toolchain.ToolEntry{
		{
			Name:        "golangci-lint",
			Binary:      "golangci-lint",
			InstallHint: "brew install golangci-lint",
		},
		{Name: "ruff", Binary: "ruff", Cmd: []string{"uv", "run"}},
	})
	require.Len(t, defs, 2, "DefsFromTools should return 2 defs")

	assert.Equal(t, "golangci-lint", defs[0].Name)
	assert.Equal(t, "golangci-lint", defs[0].Binary)
	assert.Equal(t, "brew install golangci-lint", defs[0].InstallHint)

	assert.Equal(t, "ruff", defs[1].Name)
	require.Len(t, defs[1].Prefix, 2, "expected 2-element prefix for ruff")
	assert.Equal(t, "uv", defs[1].Prefix[0])
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
		{Name: "buf", Binary: "buf"},
		{Name: "ruff", Binary: "ruff"},
	})
	assert.Equal(t, 2, reg.Len(), "Len() after 2 distinct registrations")

	// Re-register "buf" - should overwrite, not add.
	reg.Register(toolchain.NewTool("buf-v2", "buf", "", nil))
	assert.Equal(t, 2, reg.Len(), "Len() after overwrite should remain 2")
}
