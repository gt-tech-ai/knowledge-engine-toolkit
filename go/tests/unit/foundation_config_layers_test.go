package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/repos"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/stores"
)

// TestLayerConfigDefaults_MatchConsts tests the stores/repos config defaults.
//
// Why this test is important:
//   - The layer configs replace constants with a config surface; a wrong default would
//     silently shift page sizes or cache TTLs the moment a provider wires them in.
//
// What it tests:
//   - The default store kind (ent), page size (20) and cap (1000), and the cache TTL
//     (5m) + version (1), match the values the code used as consts.
func TestLayerConfigDefaults_MatchConsts(t *testing.T) {
	t.Parallel()

	s := stores.DefaultConfig()
	assert.Equal(t, "ent", s.Kind)
	assert.Equal(t, 20, s.DefaultPageSize)
	assert.Equal(t, 1000, s.MaxPageSize)

	r := repos.DefaultConfig()
	assert.Equal(t, 5*time.Minute, r.Caching.TTL)
	assert.Equal(t, 1, r.Caching.Version)
}

// TestLayerConfigValidate_RejectsBadValues tests that each layer's Validate()
// rejects the out-of-range values it names.
//
// Why this test is important:
//   - These surfaces are meant to be tuned per environment; Validate guards a
//     fat-fingered overlay (a zero page size, a max below the default) from
//     producing a silently-broken data layer.
//
// What it tests:
//   - Defaults validate; a zero default page size and a max_page_size below the
//     default are each rejected.
func TestLayerConfigValidate_RejectsBadValues(t *testing.T) {
	t.Parallel()

	require.NoError(t, stores.DefaultConfig().Validate())
	require.NoError(t, repos.DefaultConfig().Validate())

	badPage := stores.DefaultConfig()
	badPage.DefaultPageSize = 0
	require.Error(t, badPage.Validate())

	badMax := stores.DefaultConfig()
	badMax.MaxPageSize = 5 // below default_page_size 20
	assert.Error(t, badMax.Validate())
}
