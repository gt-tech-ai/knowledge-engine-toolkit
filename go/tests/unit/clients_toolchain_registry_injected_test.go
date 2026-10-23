package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestClientsToolchainCatalog_FromInjectedDefs tests that a registry built from injected
// defs resolves tools the way the former package-level toolchain.Get did.
//
// Why this test is important:
//   - The tool lookup is now an injected value, not a process global; each front-end or
//     test builds its own from its own manifest, so the built registry must carry the
//     registered prefix and keep the fallback for unknown binaries.
//
// What it tests:
//   - NewRegistryFromDefs(DefsFromTools(tools)) keeps a registered tool's install hint
//     and returns the `uv run` prefix for a registered ruff;
//   - an unregistered binary yields a minimal fallback tool named after it, with no
//     prefix.
func TestClientsToolchainCatalog_FromInjectedDefs(t *testing.T) {
	t.Parallel()

	reg := toolchain.NewRegistryFromDefs(toolchain.DefsFromTools([]types.ToolEntry{
		{Name: "ruff", Binary: "ruff", Cmd: []string{"uv", "run"}},
	}))

	exe, args := reg.Get("ruff").Cmd("check")
	assert.Equal(t, "uv", exe)
	require.Equal(t, []string{"run", "ruff", "check"}, args)

	fallback := reg.Get("nope")
	assert.Equal(t, "nope", fallback.Binary())
	exe, args = fallback.Cmd("x")
	assert.Equal(t, "nope", exe)
	assert.Equal(t, []string{"x"}, args)
}
