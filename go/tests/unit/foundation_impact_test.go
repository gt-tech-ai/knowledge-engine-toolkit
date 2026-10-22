package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/impact"
)

// TestFoundationImpact_Affected tests changed-path to unit mapping.
//
// Why this test is important:
//   - A merge applies exactly the deploy rows it touched: a missed row leaves an
//     environment stale, an extra one applies a change nobody reviewed for it, and a
//     dispatch-only row must never apply on a merge.
//
// What it tests:
//   - An owned path selects its unit; a shared path selects every unit except the
//     dispatch-only ones; nested prefixes select both owners; a prefix does not match
//     a sibling that merely shares its name; no changes select nothing; the result is
//     sorted.
func TestFoundationImpact_Affected(t *testing.T) {
	t.Parallel()
	m := impact.Map[string]{
		Owned: map[string][]string{
			"staging": {"envs/staging"}, "partner": {"envs/partner"},
			"global": {"envs/global"}, "edge": {"envs/staging/edge"},
		},
		Shared:       []string{"modules/"},
		DispatchOnly: []string{"global"},
	}
	assert.Equal(t, []string{"partner"}, m.Affected([]string{"envs/partner/main.tf"}))
	assert.Equal(t, []string{"edge", "partner", "staging"},
		m.Affected([]string{"modules/vpc/main.tf"}))
	assert.Equal(
		t,
		[]string{"edge", "staging"},
		m.Affected([]string{"envs/staging/edge/x"}),
	)
	assert.Empty(t, m.Affected([]string{"envs/staging-old/x", "envs/global/a.tf"}))
	assert.Empty(t, m.Affected(nil))
}
