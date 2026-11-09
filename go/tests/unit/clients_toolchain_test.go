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

	registry := toolchain.NewRegistry()
	assert.Equal(t, 0, registry.Len())
	registry.Register(toolchain.NewTool(textGolangciLint, textGolangciLint,
		"brew install golangci-lint", nil))
	registry.Register(
		toolchain.NewTool(textRuff, textRuff, "", []string{textUv, textRun}),
	)
	assert.Equal(t, wantLen, registry.Len())

	got := registry.Get(textGolangciLint)
	assert.Equal(t, textGolangciLint, got.Name())
	assert.Equal(t, textGolangciLint, got.Binary())
	assert.Equal(t, "nonexistent", registry.Get("nonexistent").Binary())

	exe, args := registry.Get(textRuff).Cmd(textCheck, ".")
	assert.Equal(t, textUv, exe)
	assert.Equal(t, []string{textRun, textRuff, textCheck, "."}, args)

	exe, args = got.Cmd(textRun)
	assert.Equal(t, textGolangciLint, exe)
	assert.Equal(t, []string{textRun}, args)
}
