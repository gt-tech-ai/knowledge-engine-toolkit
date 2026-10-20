package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
)

// TestClientsToolchainRegistry_GetAndCmd tests registering, resolving and invoking tools.
//
// Why this test is important:
//   - Every tool run resolves its tool by name and builds its command line from the
//     tool's spec; a wrong binary or a dropped prefix (`uv run`) runs the wrong program.
//
// What it tests:
//   - A registered tool resolves by name to its binary and Len counts it; an unknown
//     name falls back to a spec whose binary is the name; Cmd puts a prefix's first
//     word in front as the executable and the rest before the arguments, and runs a
//     tool without a prefix directly.
func TestClientsToolchainRegistry_GetAndCmd(t *testing.T) {
	t.Parallel()
	r := toolchain.NewRegistry()
	assert.Equal(t, 0, r.Len())
	r.Register(toolchain.NewTool("golangci-lint", "golangci-lint",
		"brew install golangci-lint", nil))
	r.Register(toolchain.NewTool("ruff", "ruff", "", []string{"uv", "run"}))
	assert.Equal(t, 2, r.Len())

	got := r.Get("golangci-lint")
	assert.Equal(t, "golangci-lint", got.Name())
	assert.Equal(t, "golangci-lint", got.Binary())
	assert.Equal(t, "nonexistent", r.Get("nonexistent").Binary())

	exe, args := r.Get("ruff").Cmd("check", ".")
	assert.Equal(t, "uv", exe)
	assert.Equal(t, []string{"run", "ruff", "check", "."}, args)

	exe, args = got.Cmd("run")
	assert.Equal(t, "golangci-lint", exe)
	assert.Equal(t, []string{"run"}, args)
}
