package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// namedJob is a passing local job named name in group.
func namedJob(name, group string) interfaces.AnyJob {
	return job.Base{}.Local(
		name,
		group,
		func(context.Context) (types.StepResults, error) {
			return types.OneStep(name, types.StatusPass, nil), nil
		},
	)
}

// TestServicesSuite_Composite tests a composite gate over suites and named jobs.
//
// Why this test is important:
//   - a preflight level is a composite: an ordered union of suites, each under its own
//     flags, plus the jobs the product still builds itself. Its order is the contract
//     (format before lint, the clean-tree check last), a phase that builds no job must
//     vanish rather than run empty, and a name the data gets wrong must fail the gate
//     loudly, not drop a check.
//
// What it tests:
//   - phases keep the spec's order and serial flag; a suite reference's jobs fold into
//     its phase in the order the product returns them, beside the phase's named jobs;
//   - a phase whose suites and jobs build nothing is dropped; a nil-building job is
//     omitted;
//   - an unknown job name is one failing INVALID_INPUT job naming the composite and the
//     name; a suite reference with no Suites func is likewise one failing job.
func TestServicesSuite_Composite(t *testing.T) {
	t.Parallel()
	var refs []types.SuiteRef
	c := suite.Composite{
		Decs: fixtures.Decorations(),
		Spec: types.Composite{Title: "Preflight", Phases: []types.CompositePhase{
			{Group: "Toolchain", Jobs: []string{"toolchain"}, Serial: true},
			{Group: "Format", Suites: []types.SuiteRef{{Name: "fmt"}}, Serial: true},
			{Group: "Lint & Type Check", Suites: []types.SuiteRef{
				{Name: "lint", Flags: []string{"go", "offline"}},
				{Name: "check", Select: &types.Selection{Tags: []string{"mocks"}}},
			}, Jobs: []string{"trivy"}},
			{Group: "Unit Tests", Jobs: []string{"go-unit", "ts-unit"}},
		}},
		Suites: func(ref types.SuiteRef) []interfaces.AnyJob {
			refs = append(refs, ref)
			return []interfaces.AnyJob{
				namedJob(ref.Name+" a", ref.Name),
				namedJob(ref.Name+" b", ref.Name),
			}
		},
		Jobs: map[string]func() interfaces.AnyJob{
			"toolchain": func() interfaces.AnyJob { return nil },
			"trivy":     func() interfaces.AnyJob { return namedJob("trivy license", "x") },
			"go-unit":   func() interfaces.AnyJob { return namedJob("Go tests", "x") },
			"ts-unit":   func() interfaces.AnyJob { return namedJob("TypeScript tests", "x") },
		},
	}
	g := c.Gate()
	phases := g.Phases()
	require.Len(t, phases, 3, "the empty Toolchain phase is dropped")
	names := func(i int) []string {
		out := make([]string, 0, len(phases[i].Jobs))
		for _, j := range phases[i].Jobs {
			out = append(out, j.Meta().Name)
		}
		return out
	}
	assert.Equal(t, "Format", phases[0].Name)
	assert.Equal(t, []string{"fmt a", "fmt b"}, names(0))
	assert.Equal(t, "Lint & Type Check", phases[1].Name)
	assert.Equal(t, []string{"lint a", "lint b", "check a", "check b", "trivy license"},
		names(1))
	assert.Equal(t, "Unit Tests", phases[2].Name)
	assert.Equal(t, []string{"Go tests", "TypeScript tests"}, names(2))
	assert.Equal(t, []types.SuiteRef{
		{Name: "fmt"},
		{Name: "lint", Flags: []string{"go", "offline"}},
		{Name: "check", Select: &types.Selection{Tags: []string{"mocks"}}},
	}, refs)
	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	require.NoError(t, g.Run(t.Context(), runner, "/r"))

	bad := suite.Composite{
		Decs: fixtures.Decorations(),
		Spec: types.Composite{Title: "Preflight", Phases: []types.CompositePhase{
			{Group: "Unit Tests", Jobs: []string{"rust-unit"}},
			{Group: "Format", Suites: []types.SuiteRef{{Name: "fmt"}}},
		}},
	}
	bg := bad.Gate()
	require.Len(t, bg.Phases(), 2)
	err := bg.Run(t.Context(), runner, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(apperr.CodeInvalidInput))
	assert.Contains(t, err.Error(), `Preflight: no job "rust-unit"`)
	assert.Contains(t, err.Error(), `Preflight: no suites for "fmt"`)
}
