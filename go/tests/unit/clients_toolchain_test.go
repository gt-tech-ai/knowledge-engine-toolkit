package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
)

// --- Registry ---

func TestClientsToolchainRegistry_RegisterAndGet(t *testing.T) {
	r := toolchain.NewRegistry()
	r.Register(toolchain.NewTool(
		"golangci-lint",
		"golangci-lint",
		"brew install golangci-lint",
		nil,
	),
	)

	got := r.Get("golangci-lint")
	assert.Equal(t, "golangci-lint", got.Binary())
	assert.Equal(t, "golangci-lint", got.Name())
}

func TestClientsToolchainRegistry_GetMissing_ReturnsFallback(t *testing.T) {
	r := toolchain.NewRegistry()
	got := r.Get("nonexistent")
	assert.Equal(t, "nonexistent", got.Binary())
}

func TestClientsToolchainRegistry_Len(t *testing.T) {
	r := toolchain.NewRegistry()
	assert.Equal(t, 0, r.Len(), "new registry should have Len 0")
	r.Register(toolchain.NewTool("buf", "buf", "", nil))
	assert.Equal(t, 1, r.Len(), "registry should have Len 1 after one registration")
}

func TestClientsToolchainRegistry_Cmd_WithPrefix(t *testing.T) {
	r := toolchain.NewRegistry()
	r.Register(toolchain.NewTool("ruff", "ruff", "", []string{"uv", "run"}))

	tool := r.Get("ruff")
	exe, args := tool.Cmd("check", ".")
	assert.Equal(t, "uv", exe)
	want := []string{"run", "ruff", "check", "."}
	require.Equal(t, want, args, "args mismatch")
}

func TestClientsToolchainRegistry_Cmd_WithoutPrefix(t *testing.T) {
	r := toolchain.NewRegistry()
	r.Register(toolchain.NewTool("buf", "buf", "", nil))

	tool := r.Get("buf")
	exe, args := tool.Cmd("lint")
	assert.Equal(t, "buf", exe)
	require.Len(t, args, 1)
	assert.Equal(t, "lint", args[0])
}

// --- arg profiles ---

func TestClientsToolchainGolangciLintArgs_WithoutFix(t *testing.T) {
	t.Parallel()
	args := toolchain.GolangciLintArgs(false)
	require.NotEmpty(t, args, "GolangciLintArgs returned empty slice")
	assert.Equal(t, "run", args[0])
	assert.NotContains(t, args, "--fix", "--fix should not appear when fix=false")
}

func TestClientsToolchainGolangciLintArgs_WithFix(t *testing.T) {
	t.Parallel()
	args := toolchain.GolangciLintArgs(true)
	assert.Contains(t, args, "--fix", "expected --fix in args")
}

func TestClientsToolchainRuffCheckArgs_FixAndFast(t *testing.T) {
	t.Parallel()
	args := toolchain.RuffCheckArgs(true, true)
	assert.Contains(t, args, "--fix", "expected --fix")
	assert.Contains(t, args, "--select", "expected --select")
}
