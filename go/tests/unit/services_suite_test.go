package unit_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
		{
			Name:  textGolangciLint,
			Verb:  textLint,
			Group: textLint2,
			Tags:  []string{textGo2},
		},
		{
			Name:  textRuff,
			Verb:  textLint,
			Group: textLint2,
			Tags:  []string{textPython, textSlow},
		},
		{
			Name:  textLychee,
			Verb:  textLint,
			Group: textDocsLint,
			Tags:  []string{textDocs2, textOnline},
		},
		{Name: textGofumpt, Verb: textFmt, Group: textFormat, Tags: []string{textGo2}},
	}
}

// lintSuite is a lint suite over suiteRuns.
func lintSuite() types.Suite {
	lint := func(tag string) types.Selection {
		return types.Selection{Verbs: []string{textLint}, Tags: []string{tag}}
	}

	return types.Suite{
		Title: textLint2,
		Select: types.Selection{
			Verbs:   []string{textLint},
			AnyTags: []string{textGo2, textPython},
		},
		Phases: []types.Phase{{Group: textDocsLint}, {Group: textLint2}},
		Selectors: map[string]types.Selection{
			textGo2: lint(
				textGo2,
			),
			textPython: lint(textPython),
			textDocs2:  lint(textDocs2),
		},
		Modifiers: map[string]types.Selection{
			textFast:    {ExcludeTags: []string{textSlow}},
			textOffline: {ExcludeTags: []string{textOnline}},
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

	suiteValue := lintSuite()
	catalog := suite.Runs(nil, nil, suiteRuns())
	names := func(s types.Suite, cat []suite.Entry, flags ...string) []string {
		got, err := suite.Select(&s, cat, flags)
		require.NoError(t, err)

		return entryNames(got)
	}
	assert.Equal(t, []string{textGolangciLint, textRuff}, names(suiteValue, catalog))
	assert.Equal(
		t,
		[]string{textGolangciLint, textRuff},
		names(suiteValue, catalog, textPython, textGo2),
	)
	assert.Equal(
		t,
		[]string{textGolangciLint},
		names(suiteValue, catalog, textGo2, textFast),
	)
	assert.Equal(t, []string{textLychee}, names(suiteValue, catalog, textDocs2))
	assert.Empty(t, names(suiteValue, catalog, textDocs2, textOffline))

	_, err := suite.Select(&suiteValue, catalog, []string{textRust})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, textRust)

	gated := suiteValue

	gated.Gated = map[string]string{textOffline: textOffline}

	testServicesSuiteSelectComposesFlagsRest(t, &suiteValue, catalog, names, &gated)
}

// testServicesSuiteSelectComposesFlagsRest continues
// TestServicesSuite_SelectComposesFlags with its remaining steps.
func testServicesSuiteSelectComposesFlagsRest(
	t *testing.T,
	suiteValue *types.Suite,
	catalog []suite.Entry,
	names func(s types.Suite, cat []suite.Entry, flags ...string) []string,
	gated *types.Suite,
) {
	t.Helper()

	var err error

	offline := suite.Runs(nil, nil, []types.ToolRun{
		{
			Name:  textLychee,
			Verb:  textLint,
			Group: textDocsLint,
			Tags:  []string{textDocs2, textOnline},
		},
		{
			Name:  "lychee offline",
			Verb:  textLint,
			Group: textDocsLint,
			Tags:  []string{textDocs2, textOffline},
		},
	})
	assert.Equal(t, []string{textLychee}, names(*gated, offline, textDocs2))
	assert.Equal(
		t,
		[]string{"lychee offline"},
		names(*gated, offline, textDocs2, textOffline),
	)

	steps := suite.Steps(map[string]suite.Step{
		textLintVet: {Group: textLint2, Tags: []string{textGo2}},
		"lint.off":  {Group: textLint2, Tags: []string{textGo2}},
	})
	withSteps := *suiteValue

	withSteps.Steps = []string{textLintVet}

	all := slices.Concat(catalog, steps)
	assert.Equal(
		t,
		[]string{textGolangciLint, textLintVet},
		names(withSteps, all, textGo2),
	)

	withSteps.Steps = []string{"lint.nope"}
	_, err = suite.Select(&withSteps, all, nil)
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

	suiteValue := lintSuite()

	suiteValue.Phases[1].Serial = true

	selected, err := suite.Select(
		&suiteValue,
		suite.Runs(nil, nil, suiteRuns()),
		[]string{textGo2, textDocs2},
	)
	require.NoError(t, err)

	plan, err := suite.Plan(&suiteValue, selected)
	require.NoError(t, err)
	require.Len(t, plan, wantPlanLen2)
	assert.Equal(t, textDocsLint, plan[0].Group)
	assert.False(t, plan[0].Serial)
	assert.Equal(t, textLychee, plan[0].Entries[0].Name)
	assert.Equal(t, textLint2, plan[1].Group)
	assert.True(t, plan[1].Serial)

	_, err = suite.Plan(&suiteValue, suite.Runs(nil, nil, suiteRuns()[3:]))
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, textGofumpt)
	assert.ErrorContains(t, err, textFormat)
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

	testServicesSuiteGateRest6(t, ctrl, tr)
}

// testServicesSuiteGateRest6 continues testServicesSuiteGateRest5 with its remaining
// steps.
func testServicesSuiteGateRest6(
	t *testing.T,
	ctrl *gomock.Controller,
	toolRunner *mocks.MockToolRunner,
) {
	t.Helper()

	var (
		inFlight, peak atomic.Int32
		lock           sync.Mutex
		ran            []string
	)

	toolRunner.EXPECT().
		Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Times(2).
		DoAndReturn(func(
			_ context.Context, _ interfaces.Codebase, rRef *types.ToolRun, _ types.RunOptions,
		) (types.StepResults, error) {
			toolRun := *rRef

			n := inFlight.Add(1)
			recordPeak(&peak, n)

			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			lock.Lock()

			ran = append(ran, toolRun.Name)
			lock.Unlock()

			return types.OneStep(toolRun.Name, types.StatusPass, nil), nil
		})

	runner := mocks.NewMockCommandRunner(ctrl)

	testServicesSuiteGateRest2(t, toolRunner, &peak, &lock, &ran, runner)
}

// testServicesSuiteGateRest2 continues testServicesSuiteGateRest6 with its remaining
// steps.
func testServicesSuiteGateRest2(
	t *testing.T,
	toolRunner *mocks.MockToolRunner,
	peak *atomic.Int32,
	lock *sync.Mutex,
	ran *[]string,
	runner *mocks.MockCommandRunner,
) {
	t.Helper()

	suiteValue := types.Suite{
		Title:  textLint2,
		Select: types.Selection{Verbs: []string{textLint}},
		Phases: []types.Phase{{Group: textFormat, Serial: true}, {Group: textLint2}},
		Selectors: map[string]types.Selection{
			textGo2:    {Verbs: []string{textLint}, Tags: []string{textGo2}},
			textPython: {Verbs: []string{textLint}, Tags: []string{textPython}},
		},
		Steps: []string{textLintVet},
	}
	catalog := append(suite.Runs(toolRunner, nil, []types.ToolRun{
		{Name: textGofumpt, Verb: textLint, Group: textFormat, Tags: []string{textGo2}},
		{Name: "golines", Verb: textLint, Group: textFormat, Tags: []string{textGo2}},
		{Name: textRuff, Verb: textLint, Group: textLint2, Tags: []string{textPython}},
	}), suite.Steps(map[string]suite.Step{textLintVet: {
		Group: textLint2, Tags: []string{textGo2},
		Run: func(_ context.Context, env *suite.StepEnv) (types.StepResults, error) {
			assert.Same(t, runner, asType[*mocks.MockCommandRunner](t, env.Runner))
			lock.Lock()

			(*ran) = append((*ran), "lint.vet@"+env.Root)
			lock.Unlock()

			return types.OneStep(textVet, types.StatusPass, nil), nil
		},
	}})...)
	env := suite.Env{Decs: fixtures.Decorations()}
	gate := (&suite.Gate{
		Env: env, Suite: suiteValue, Catalog: catalog,
		Flags: []string{textGo2},
	}).Gate()
	phases := make([]string, 0, capacity2)

	for _, p := range gate.Phases() {
		phases = append(phases, p.Name)
	}

	assert.Equal(t, []string{textFormat, textLint2}, phases)
	require.NoError(t, gate.Run(t.Context(), runner, textR))
	assert.Equal(t, []string{textGofumpt, "golines", "lint.vet@/r"}, (*ran))
	assert.Equal(t, int32(1), peak.Load(), "a serial phase runs one row at a time")

	testServicesSuiteGateRest(t, toolRunner, runner, &suiteValue, catalog, env)
}

// testServicesSuiteGateRest continues TestServicesSuite_Gate with its remaining steps.
func testServicesSuiteGateRest(
	t *testing.T,
	toolRunner *mocks.MockToolRunner,
	runner *mocks.MockCommandRunner,
	suiteValue *types.Suite,
	catalog []suite.Entry,
	env suite.Env,
) {
	t.Helper()

	plain := types.ToolRun{Name: textHadolint, Verb: textLint}
	toolRunner.EXPECT().Run(gomock.Any(), nil, any(&plain), types.RunOptions{Fix: true}).
		Return(nil, apperr.New(apperr.CodeInvalidInput, "no value for {image}"))

	j := suite.Runs(toolRunner, nil, []types.ToolRun{plain})[0].Job(suite.Env{
		Decs: fixtures.Decorations(), Opts: types.RunOptions{Fix: true},
	})
	assert.Equal(t, types.JobMeta{Name: textHadolint, Group: textLint}, j.Meta())

	got, err := j.Execute(t.Context(), runner, textR)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusFail, got[0].Status)
	assert.Contains(t, got[0].Error, string(apperr.CodeInvalidInput))

	bad := suite.Gate{
		Env:     env,
		Suite:   *suiteValue,
		Catalog: catalog,
		Flags:   []string{textRust},
	}
	require.Error(t, bad.Gate().Run(t.Context(), runner, textR))

	jobs := bad.Jobs()
	require.Len(t, jobs, 1)

	res, _ := jobs[0].Execute(t.Context(), runner, textR)
	require.Len(t, res, 1)
	assert.Contains(t, res[0].Error, textRust)
}

// recordPeak raises peak to n unless a concurrent caller already raised it further.
func recordPeak(peak *atomic.Int32, n int32) {
	for {
		seen := peak.Load()
		if n <= seen || peak.CompareAndSwap(seen, n) {
			return
		}
	}
}
