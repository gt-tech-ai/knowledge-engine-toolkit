package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
		fixtures.Build(func(row *types.ToolEntry) {
			row.Name = textRuff
			row.Binary = textRuff
			row.Cmd = []string{textUv, textRun}
		}),
	}))

	exe, args := reg.Get(textRuff).Cmd(textCheck)
	assert.Equal(t, textUv, exe)
	require.Equal(t, []string{textRun, textRuff, textCheck}, args)

	fallback := reg.Get(textNope)
	assert.Equal(t, textNope, fallback.Binary())

	exe, args = fallback.Cmd(textX)
	assert.Equal(t, textNope, exe)
	assert.Equal(t, []string{textX}, args)
}
