package unit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// suiteRuns is a four-row catalog: Go and Python lint (Python tagged slow), a docs row
// that reaches the network, and a fmt row no lint selection may pick.
func suiteRuns() []types.ToolRun {
	return []types.ToolRun{
		{Name: "golangci-lint", Verb: "lint", Group: "Lint", Tags: []string{"go"}},
		{Name: "ruff", Verb: "lint", Group: "Lint", Tags: []string{"python", "slow"}},
		{
			Name:  "lychee",
			Verb:  "lint",
			Group: "Docs Lint",
			Tags:  []string{"docs", "online"},
		},
		{Name: "gofumpt", Verb: "fmt", Group: "Format", Tags: []string{"go"}},
	}
}

// lintSuite is a lint suite over suiteRuns.
func lintSuite() types.Suite {
	lint := func(tag string) types.Selection {
		return types.Selection{Verbs: []string{"lint"}, Tags: []string{tag}}
	}
	return types.Suite{
		Title: "Lint",
		Select: types.Selection{
			Verbs:   []string{"lint"},
			AnyTags: []string{"go", "python"},
		},
		Phases: []types.Phase{{Group: "Docs Lint"}, {Group: "Lint"}},
		Selectors: map[string]types.Selection{
			"go": lint("go"), "python": lint("python"), "docs": lint("docs"),
		},
		Modifiers: map[string]types.Selection{
			"fast":    {ExcludeTags: []string{"slow"}},
			"offline": {ExcludeTags: []string{"online"}},
		},
	}
}

// entryNames returns the names of es.
func entryNames(es []suite.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

// TestServicesSuite_SelectComposesFlags tests the suite flag algebra.
//
// Why this test is important:
//   - `lint --go --python` must run both languages and `--go --fast` must drop slow
//     rows; the algebra is the contract every suite-backed command's flags obey, and an
//     offline-only row must stay out until its flag is set (a modifier only narrows).
//
// What it tests:
//   - No flag selects the suite's Select; selector flags union; modifiers intersect;
//     catalog order is kept; an unknown flag is INVALID_INPUT naming it.
//   - A gated tag's entry runs only under its flag.
//   - A step entry runs only when the suite's Steps names it; a listed step missing from
//     the catalog is INVALID_INPUT.
func TestServicesSuite_SelectComposesFlags(t *testing.T) {
	t.Parallel()
	s := lintSuite()
	catalog := suite.Runs(nil, nil, suiteRuns())
	names := func(s types.Suite, cat []suite.Entry, flags ...string) []string {
		got, err := suite.Select(s, cat, flags)
		require.NoError(t, err)
		return entryNames(got)
	}
	assert.Equal(t, []string{"golangci-lint", "ruff"}, names(s, catalog))
	assert.Equal(t, []string{"golangci-lint", "ruff"}, names(s, catalog, "python", "go"))
	assert.Equal(t, []string{"golangci-lint"}, names(s, catalog, "go", "fast"))
	assert.Equal(t, []string{"lychee"}, names(s, catalog, "docs"))
	assert.Empty(t, names(s, catalog, "docs", "offline"))
	_, err := suite.Select(s, catalog, []string{"rust"})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "rust")

	gated := s
	gated.Gated = map[string]string{"offline": "offline"}
	offline := suite.Runs(nil, nil, []types.ToolRun{
		{
			Name:  "lychee",
			Verb:  "lint",
			Group: "Docs Lint",
			Tags:  []string{"docs", "online"},
		},
		{
			Name:  "lychee offline",
			Verb:  "lint",
			Group: "Docs Lint",
			Tags:  []string{"docs", "offline"},
		},
	})
	assert.Equal(t, []string{"lychee"}, names(gated, offline, "docs"))
	assert.Equal(t, []string{"lychee offline"}, names(gated, offline, "docs", "offline"))

	steps := suite.Steps(map[string]suite.Step{
		"lint.vet": {Group: "Lint", Tags: []string{"go"}},
		"lint.off": {Group: "Lint", Tags: []string{"go"}},
	})
	withSteps := s
	withSteps.Steps = []string{"lint.vet"}
	all := append(append([]suite.Entry{}, catalog...), steps...)
	assert.Equal(t, []string{"golangci-lint", "lint.vet"}, names(withSteps, all, "go"))
	withSteps.Steps = []string{"lint.nope"}
	_, err = suite.Select(withSteps, all, nil)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.ErrorContains(t, err, "lint.nope")
}

// TestServicesSuite_PlanOrdersPhases tests a suite's phase plan.
//
// Why this test is important:
//   - A gate's phases run in order and writers to one tree run one at a time; the
//     suite's Phases, not the catalog order, decide both, and an entry whose group no
//     phase names would silently never run.
//
// What it tests:
//   - Plan groups entries into the suite's phases in Phases order, keeps Serial, and
//     drops an empty phase; an unphased group is INVALID_INPUT naming entry and group.
func TestServicesSuite_PlanOrdersPhases(t *testing.T) {
	t.Parallel()
	s := lintSuite()
	s.Phases[1].Serial = true
	selected, err := suite.Select(
		s,
		suite.Runs(nil, nil, suiteRuns()),
		[]string{"go", "docs"},
	)
	require.NoError(t, err)
	plan, err := suite.Plan(s, selected)
	require.NoError(t, err)
	require.Len(t, plan, 2)
	assert.Equal(t, "Docs Lint", plan[0].Group)
	assert.False(t, plan[0].Serial)
	assert.Equal(t, "lychee", plan[0].Entries[0].Name)
	assert.Equal(t, "Lint", plan[1].Group)
	assert.True(t, plan[1].Serial)

	_, err = suite.Plan(s, suite.Runs(nil, nil, suiteRuns()[3:]))
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "gofumpt")
	assert.ErrorContains(t, err, "Format")
}

// TestServicesSuite_Gate tests the gate a suite builds.
//
// Why this test is important:
//   - Every suite-backed command is this gate; the wrong rows, phases out of order, two
//     writers of one tree at once, or a row that could not run aborting the gate would
//     change what developers and CI run.
//
// What it tests (the tool runner is a generated mock):
//   - --go runs the Go rows and the Go step only, in phase order; a serial phase never
//     overlaps (the mock counts concurrent calls).
//   - A row job is named after the row and grouped by its Group, else its verb; a
//     runner error is one failed step naming the row and the error's code.
//   - A step runs against the gate's runner and root.
//   - An unknown flag is one failing job naming the flag, from Gate and from Jobs.
func TestServicesSuite_Gate(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	tr := mocks.NewMockToolRunner(ctrl)
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	var ran []string
	tr.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(2).
		DoAndReturn(func(
			_ context.Context, _ interfaces.Codebase, r types.ToolRun, _ types.RunOptions,
		) (types.StepResults, error) {
			n := inFlight.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			mu.Lock()
			ran = append(ran, r.Name)
			mu.Unlock()
			return types.OneStep(r.Name, types.StatusPass, nil), nil
		})
	runner := mocks.NewMockCommandRunner(ctrl)
	s := types.Suite{
		Title:  "Lint",
		Select: types.Selection{Verbs: []string{"lint"}},
		Phases: []types.Phase{{Group: "Format", Serial: true}, {Group: "Lint"}},
		Selectors: map[string]types.Selection{
			"go":     {Verbs: []string{"lint"}, Tags: []string{"go"}},
			"python": {Verbs: []string{"lint"}, Tags: []string{"python"}},
		},
		Steps: []string{"lint.vet"},
	}
	catalog := append(suite.Runs(tr, nil, []types.ToolRun{
		{Name: "gofumpt", Verb: "lint", Group: "Format", Tags: []string{"go"}},
		{Name: "golines", Verb: "lint", Group: "Format", Tags: []string{"go"}},
		{Name: "ruff", Verb: "lint", Group: "Lint", Tags: []string{"python"}},
	}), suite.Steps(map[string]suite.Step{"lint.vet": {
		Group: "Lint", Tags: []string{"go"},
		Run: func(_ context.Context, env suite.StepEnv) (types.StepResults, error) {
			assert.Same(t, runner, env.Runner.(*mocks.MockCommandRunner))
			mu.Lock()
			ran = append(ran, "lint.vet@"+env.Root)
			mu.Unlock()
			return types.OneStep("vet", types.StatusPass, nil), nil
		},
	}})...)
	env := suite.Env{Decs: fixtures.Decorations()}
	g := suite.Gate{Env: env, Suite: s, Catalog: catalog, Flags: []string{"go"}}.Gate()
	phases := make([]string, 0, 2)
	for _, p := range g.Phases() {
		phases = append(phases, p.Name)
	}
	assert.Equal(t, []string{"Format", "Lint"}, phases)
	require.NoError(t, g.Run(t.Context(), runner, "/r"))
	assert.Equal(t, []string{"gofumpt", "golines", "lint.vet@/r"}, ran)
	assert.Equal(t, int32(1), peak.Load(), "a serial phase runs one row at a time")

	plain := types.ToolRun{Name: "hadolint", Verb: "lint"}
	tr.EXPECT().Run(gomock.Any(), nil, plain, types.RunOptions{Fix: true}).
		Return(nil, apperr.New(apperr.CodeInvalidInput, "no value for {image}"))
	j := suite.Runs(tr, nil, []types.ToolRun{plain})[0].Job(suite.Env{
		Decs: fixtures.Decorations(), Opts: types.RunOptions{Fix: true},
	})
	assert.Equal(t, types.JobMeta{Name: "hadolint", Group: "lint"}, j.Meta())
	got, err := j.Execute(t.Context(), runner, "/r")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusFail, got[0].Status)
	assert.Contains(t, got[0].Error, string(apperr.CodeInvalidInput))

	bad := suite.Gate{Env: env, Suite: s, Catalog: catalog, Flags: []string{"rust"}}
	require.Error(t, bad.Gate().Run(t.Context(), runner, "/r"))
	jobs := bad.Jobs()
	require.Len(t, jobs, 1)
	res, _ := jobs[0].Execute(t.Context(), runner, "/r")
	require.Len(t, res, 1)
	assert.Contains(t, res[0].Error, "rust")
}
