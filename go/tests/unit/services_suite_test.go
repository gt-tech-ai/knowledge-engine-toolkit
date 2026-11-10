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
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textGolangciLint
			row.Verb = textLint
			row.Group = textLintTitle
			row.Tags = []string{textGoLower}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textRuff
			row.Verb = textLint
			row.Group = textLintTitle
			row.Tags = []string{textPython, textSlow}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textLychee
			row.Verb = textLint
			row.Group = textDocsLint
			row.Tags = []string{textDocsLower, textOnline}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textGofumpt
			row.Verb = textFmt
			row.Group = textFormat
			row.Tags = []string{textGoLower}
		}),
	}
}

// lintSuite is a lint suite over suiteRuns.
func lintSuite() types.Suite {
	lint := func(tag string) types.Selection {
		return fixtures.Build(func(row *types.Selection) {
			row.Verbs = []string{textLint}
			row.Tags = []string{tag}
		})
	}

	return types.Suite{
		Title: textLintTitle,
		Select: fixtures.Build(func(row *types.Selection) {
			row.Verbs = []string{textLint}
			row.AnyTags = []string{textGoLower, textPython}
		}),
		Phases: []types.Phase{
			{Group: textDocsLint, Serial: false},
			{Group: textLintTitle, Serial: false},
		},
		Selectors: map[string]types.Selection{
			textGoLower: lint(
				textGoLower,
			),
			textPython:    lint(textPython),
			textDocsLower: lint(textDocsLower),
		},
		Modifiers: map[string]types.Selection{
			textFast: fixtures.Build(func(row *types.Selection) {
				row.ExcludeTags = []string{textSlow}
			}),
			textOffline: fixtures.Build(func(row *types.Selection) {
				row.ExcludeTags = []string{textOnline}
			}),
		},
		Gated: nil,
		Steps: nil,
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

	currentSuite := lintSuite()
	catalog := suite.Runs(nil, nil, suiteRuns())
	names := func(s types.Suite, cat []suite.Entry, flags ...string) []string {
		got, err := suite.Select(&s, cat, flags)
		require.NoError(t, err)

		return entryNames(got)
	}
	assert.Equal(t, []string{textGolangciLint, textRuff}, names(currentSuite, catalog))
	assert.Equal(
		t,
		[]string{textGolangciLint, textRuff},
		names(currentSuite, catalog, textPython, textGoLower),
	)
	assert.Equal(
		t,
		[]string{textGolangciLint},
		names(currentSuite, catalog, textGoLower, textFast),
	)
	assert.Equal(t, []string{textLychee}, names(currentSuite, catalog, textDocsLower))
	assert.Empty(t, names(currentSuite, catalog, textDocsLower, textOffline))

	_, err := suite.Select(&currentSuite, catalog, []string{textRust})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, textRust)

	gated := currentSuite

	gated.Gated = map[string]string{textOffline: textOffline}

	testServicesSuiteSelectComposesFlagsOffline(t, &currentSuite, catalog, names, &gated)
}

// testServicesSuiteSelectComposesFlagsOffline continues
// TestServicesSuite_SelectComposesFlags: offline.
func testServicesSuiteSelectComposesFlagsOffline(
	t *testing.T,
	currentSuite *types.Suite,
	catalog []suite.Entry,
	names func(s types.Suite, cat []suite.Entry, flags ...string) []string,
	gated *types.Suite,
) {
	t.Helper()

	var err error

	offline := suite.Runs(nil, nil, []types.ToolRun{
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textLychee
			row.Verb = textLint
			row.Group = textDocsLint
			row.Tags = []string{textDocsLower, textOnline}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = "lychee offline"
			row.Verb = textLint
			row.Group = textDocsLint
			row.Tags = []string{textDocsLower, textOffline}
		}),
	})
	assert.Equal(t, []string{textLychee}, names(*gated, offline, textDocsLower))
	assert.Equal(
		t,
		[]string{"lychee offline"},
		names(*gated, offline, textDocsLower, textOffline),
	)

	steps := suite.Steps(map[string]suite.Step{
		textLintVet: {
			Group: textLintTitle,
			Tags:  []string{textGoLower},
			Run:   nil,
			Tools: nil,
		},
		"lint.off": {
			Group: textLintTitle,
			Tags:  []string{textGoLower},
			Run:   nil,
			Tools: nil,
		},
	})
	withSteps := *currentSuite

	withSteps.Steps = []string{textLintVet}

	all := slices.Concat(catalog, steps)
	assert.Equal(
		t,
		[]string{textGolangciLint, textLintVet},
		names(withSteps, all, textGoLower),
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

	currentSuite := lintSuite()

	currentSuite.Phases[1].Serial = true

	selected, err := suite.Select(
		&currentSuite,
		suite.Runs(nil, nil, suiteRuns()),
		[]string{textGoLower, textDocsLower},
	)
	require.NoError(t, err)

	plan, err := suite.Plan(&currentSuite, selected)
	require.NoError(t, err)
	require.Len(t, plan, wantPlanLen)
	assert.Equal(t, textDocsLint, plan[0].Group)
	assert.False(t, plan[0].Serial)
	assert.Equal(t, textLychee, plan[0].Entries[0].Name)
	assert.Equal(t, textLintTitle, plan[1].Group)
	assert.True(t, plan[1].Serial)

	_, err = suite.Plan(&currentSuite, suite.Runs(nil, nil, suiteRuns()[3:]))
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

	testServicesSuiteGateRunsConcurrently(t, ctrl, tr)
}

// testServicesSuiteGateRunsConcurrently continues testServicesSuiteGate: runs
// concurrently.
func testServicesSuiteGateRunsConcurrently(
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

	testServicesSuiteGateChecksPhases(t, toolRunner, &peak, &lock, &ran, runner)
}

// testServicesSuiteGateChecksPhases continues testServicesSuiteGateRunsConcurrently:
// checks phases.
func testServicesSuiteGateChecksPhases(
	t *testing.T,
	toolRunner *mocks.MockToolRunner,
	peak *atomic.Int32,
	lock *sync.Mutex,
	ran *[]string,
	runner *mocks.MockCommandRunner,
) {
	t.Helper()

	currentSuite := types.Suite{
		Title: textLintTitle,
		Select: fixtures.Build(func(row *types.Selection) {
			row.Verbs = []string{textLint}
		}),
		Phases: []types.Phase{
			{Group: textFormat, Serial: true},
			{Group: textLintTitle, Serial: false},
		},
		Selectors: map[string]types.Selection{
			textGoLower: fixtures.Build(func(row *types.Selection) {
				row.Verbs = []string{textLint}
				row.Tags = []string{textGoLower}
			}),
			textPython: fixtures.Build(func(row *types.Selection) {
				row.Verbs = []string{textLint}
				row.Tags = []string{textPython}
			}),
		},
		Steps:     []string{textLintVet},
		Modifiers: nil,
		Gated:     nil,
	}

	testServicesSuiteGateChecksTools(
		t,
		toolRunner,
		peak,
		lock,
		ran,
		runner,
		&currentSuite,
	)
}

// testServicesSuiteGateChecksTools continues testServicesSuiteGateChecksPhases.
func testServicesSuiteGateChecksTools(
	t *testing.T,
	toolRunner *mocks.MockToolRunner,
	peak *atomic.Int32,
	lock *sync.Mutex,
	ran *[]string,
	runner *mocks.MockCommandRunner,
	currentSuiteRef *types.Suite,
) {
	t.Helper()

	currentSuite := *currentSuiteRef

	catalog := append(suite.Runs(toolRunner, nil, []types.ToolRun{
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textGofumpt
			row.Verb = textLint
			row.Group = textFormat
			row.Tags = []string{textGoLower}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = "golines"
			row.Verb = textLint
			row.Group = textFormat
			row.Tags = []string{textGoLower}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textRuff
			row.Verb = textLint
			row.Group = textLintTitle
			row.Tags = []string{textPython}
		}),
	}), suite.Steps(map[string]suite.Step{textLintVet: {
		Group: textLintTitle, Tags: []string{textGoLower},
		Run: func(_ context.Context, env *suite.StepEnv) (types.StepResults, error) {
			assert.Same(t, runner, asType[*mocks.MockCommandRunner](t, env.Runner))
			lock.Lock()

			(*ran) = append((*ran), "lint.vet@"+env.Root)
			lock.Unlock()

			return types.OneStep(textVet, types.StatusPass, nil), nil
		},
		Tools: nil,
	}})...)

	testServicesSuiteGateChecksToolsRunsGate(t,
		toolRunner,
		peak,
		ran,
		runner,
		&currentSuite,
		catalog,
	)
}

// testServicesSuiteGateChecksToolsRunsGate continues testServicesSuiteGateChecksTools.
func testServicesSuiteGateChecksToolsRunsGate(t *testing.T,
	toolRunner *mocks.MockToolRunner,
	peak *atomic.Int32,
	ran *[]string,
	runner *mocks.MockCommandRunner,
	currentSuite *types.Suite,
	catalog []suite.Entry,
) {
	t.Helper()

	env := fixtures.Build(func(row *suite.Env) {
		row.Decs = fixtures.Decorations()
	})
	gate := (&suite.Gate{
		Env: env, Suite: *currentSuite, Catalog: catalog,
		Flags: []string{textGoLower},
		Obs:   nil,
		Err:   nil,
	}).Gate()
	phases := make([]string, 0, makeInput)

	for _, p := range gate.Phases() {
		phases = append(phases, p.Name)
	}

	assert.Equal(t, []string{textFormat, textLintTitle}, phases)
	require.NoError(t, gate.Run(t.Context(), runner, textR))
	assert.Equal(t, []string{textGofumpt, "golines", "lint.vet@/r"}, (*ran))
	assert.Equal(t, int32(1), peak.Load(), "a serial phase runs one row at a time")

	testServicesSuiteGateChecksSingle(t, toolRunner, runner, currentSuite, catalog, env)
}

// testServicesSuiteGateChecksSingle continues TestServicesSuite_Gate: checks single.
func testServicesSuiteGateChecksSingle(
	t *testing.T,
	toolRunner *mocks.MockToolRunner,
	runner *mocks.MockCommandRunner,
	currentSuite *types.Suite,
	catalog []suite.Entry,
	env suite.Env,
) {
	t.Helper()

	plain := fixtures.Build(func(row *types.ToolRun) {
		row.Name = textHadolint
		row.Verb = textLint
	})
	toolRunner.EXPECT().
		Run(gomock.Any(), nil, any(&plain), fixtures.Build(func(row *types.RunOptions) {
			row.Fix = true
		})).
		Return(nil, apperr.New(apperr.CodeInvalidInput, "no value for {image}"))

	anyJob := suite.Runs(toolRunner, nil, []types.ToolRun{plain})[0].Job(suite.Env{
		Decs: fixtures.Decorations(),
		Opts: fixtures.Build(func(row *types.RunOptions) {
			row.Fix = true
		}),
		Snapshot:  nil,
		Component: "",
	})
	assert.Equal(
		t,
		types.JobMeta{Name: textHadolint, Group: textLint, Tool: "", WarnOnly: false},
		anyJob.Meta(),
	)

	got, err := anyJob.Execute(t.Context(), runner, textR)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusFail, got[0].Status)
	assert.Contains(t, got[0].Error, string(apperr.CodeInvalidInput))

	bad := suite.Gate{
		Env:     env,
		Suite:   *currentSuite,
		Catalog: catalog,
		Flags:   []string{textRust},
		Obs:     nil,
		Err:     nil,
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

const (
	// wantPlanLen is the expected plan len.
	wantPlanLen = 2
)

const (
	// textDocsLint is the literal "Docs Lint".
	textDocsLint = "Docs Lint"

	// textLintVet is the literal "lint.vet".
	textLintVet = "lint.vet"

	// textLychee is the literal "lychee".
	textLychee = "lychee"

	// textOnline is the literal "online".
	textOnline = "online"
)
