package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	fieldMap := impact.Map[string]{
		Owned: map[string][]string{
			textStaging: {"envs/staging"}, textPartner: {"envs/partner"},
			"global": {"envs/global"}, textEdge: {"envs/staging/edge"},
		},
		Shared:       []string{"modules/"},
		DispatchOnly: []string{"global"},
	}
	assert.Equal(
		t,
		[]string{textPartner},
		fieldMap.Affected([]string{"envs/partner/main.tf"}),
	)
	assert.Equal(t, []string{textEdge, textPartner, textStaging},
		fieldMap.Affected([]string{"modules/vpc/main.tf"}))
	assert.Equal(
		t,
		[]string{textEdge, textStaging},
		fieldMap.Affected([]string{"envs/staging/edge/x"}),
	)
	assert.Empty(t, fieldMap.Affected([]string{"envs/staging-old/x", "envs/global/a.tf"}))
	assert.Empty(t, fieldMap.Affected(nil))
}

// TestFoundationImpact_Classify tests changed-path classification by named pattern sets.
//
// Why this test is important:
//   - A CI run skips the jobs whose class no change touched; a class that misses its
//     files skips a job that should run, and a pattern that silently failed to compile
//     would skip it for good.
//
// What it tests:
//   - Each class is true when a changed path matches any of its patterns and false
//     otherwise, in class order; a shared pattern listed in several classes marks each.
//   - No changes marks no class; a bad pattern is CodeInvalidInput naming its class.
//   - SplitPaths splits NUL- and newline-separated git output, dropping empties.
func TestFoundationImpact_Classify(t *testing.T) {
	t.Parallel()

	classes := []impact.Class{
		{Name: textGoLower, Patterns: []string{`^shared/`, `\.go$`}},
		{Name: textDocsLower, Patterns: []string{`^shared/`, `^docs/`}},
		{Name: "charts", Patterns: []string{`^charts/`}},
	}

	got, err := impact.Classify(classes, []string{"cmd/main.go"})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, false}, got)

	got, err = impact.Classify(classes, []string{"shared/x.yaml"})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, true, false}, got)

	got, err = impact.Classify(classes, nil)
	require.NoError(t, err)
	assert.Equal(t, []bool{false, false, false}, got)

	_, err = impact.Classify(
		[]impact.Class{{Name: textBad, Patterns: []string{textOpen}}},
		[]string{textX},
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)
	assert.Contains(t, err.Error(), textBad)

	assert.Equal(t, []string{textAGo, textBCGo, textDLower},
		impact.SplitPaths("a.go\x00b/c.go\x00\nd\n"))
}

const (
	// textEdge is the literal "edge".
	textEdge = "edge"

	// textPartner is the literal "partner".
	textPartner = "partner"
)
