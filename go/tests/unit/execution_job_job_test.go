package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// okRunner returns a MockCommandRunner whose every method succeeds — the always-green
// runner the discover/map/execute tests fan work through. The trailing gomock.Any()
// matches the variadic args slice at any count.
func okRunner(t *testing.T) interfaces.CommandRunner {
	t.Helper()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	r.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	r.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(nil).AnyTimes()
	r.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(interfaces.CmdResult{}).AnyTimes()
	r.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(interfaces.CmdResult{}).AnyTimes()
	r.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	r.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return r
}

// countingRunner returns a MockCommandRunner whose Run fails (with failErr) until the
// call count reaches threshold, then succeeds — reproducing the transient-then-recover
// pattern the retry tests need. count is shared so a test can assert the attempt total.
func countingRunner(
	t *testing.T,
	count *int64,
	threshold int64,
	failErr error,
) interfaces.CommandRunner {
	t.Helper()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	r.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, string, string, ...string) error {
			if atomic.AddInt64(count, 1) < threshold {
				return failErr
			}
			return nil
		},
	).AnyTimes()
	r.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(nil).AnyTimes()
	r.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(interfaces.CmdResult{}).AnyTimes()
	r.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(interfaces.CmdResult{}).AnyTimes()
	r.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	r.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return r
}

// passJob returns a MockAnyJob named name whose Execute yields a single Pass result.
func passJob(t *testing.T, name string) interfaces.AnyJob {
	t.Helper()
	j := mocks.NewMockAnyJob(gomock.NewController(t))
	j.EXPECT().Meta().Return(types.JobMeta{Name: name}).AnyTimes()
	j.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Name: name, Status: types.StatusPass}}, nil).AnyTimes()
	return j
}

// goToolSpec returns a MockToolSpec describing the `go` tool (name/binary "go", no
// install hint, Cmd echoing its args after the binary).
func goToolSpec(t *testing.T) interfaces.ToolSpec {
	t.Helper()
	ts := mocks.NewMockToolSpec(gomock.NewController(t))
	ts.EXPECT().Name().Return("go").AnyTimes()
	ts.EXPECT().Binary().Return("go").AnyTimes()
	ts.EXPECT().InstallHint().Return("").AnyTimes()
	ts.EXPECT().Cmd(gomock.Any()).DoAndReturn(
		func(args ...string) (string, []string) { return "go", args },
	).AnyTimes()
	return ts
}

// workUnit builds a trivial WorkUnit for a module path.
func workUnit(root, modDir string) engine.WorkUnit {
	return engine.WorkUnit{
		Name: modDir,
		Dir:  root,
		Exe:  "go",
		Args: []string{"build", "./..."},
	}
}

// discoverItems returns a Discoverer that always yields items.
func discoverItems(items ...string) interfaces.Discoverer[string] {
	return interfaces.DiscovererFunc[string](
		func(_ context.Context, _ string) ([]string, error) {
			return items, nil
		},
	)
}

// --- Job[T] behaviour tests ---

// TestJob_DiscoverMapExecuteCollect tests the happy-path of the Job[T] pipeline:
// discover items, map each to a WorkUnit, fan them out, and collect one result
// per item.
//
// Why this test is important:
//   - This is the core discover -> map -> fan-out -> aggregate contract that
//     every CLI gate job is built on. If the cardinality were wrong (dropping or
//     duplicating items) entire modules would be silently skipped from lint,
//     build, or test runs without any failure surfacing.
//
// What it tests:
//   - Discovering three items produces exactly three results, each with
//     StatusPass when the runner succeeds.
func TestJob_DiscoverMapExecuteCollect(t *testing.T) {
	t.Parallel()
	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("pkg/a", "pkg/b", "pkg/c")).
		WithMapper(workUnit).
		Build()

	results, err := j.Execute(context.Background(), okRunner(t), "/repo")
	require.NoError(t, err)
	require.Len(t, results, 3)
	for _, r := range results {
		assert.Equal(t, types.StatusPass, r.Status, "step %q: expected Pass", r.Name)
	}
}

// TestJob_EmptyDiscoverReturnsSkip tests that a job which discovers no work
// items returns a single Skip result rather than passing or failing.
//
// Why this test is important:
//   - "Nothing to do" must be distinct from "everything passed": a job with no
//     applicable modules (e.g. no Python packages in a Go-only diff) should be
//     marked Skip so the report is honest, not silently green as if it had run.
//
// What it tests:
//   - With an empty discoverer, Execute returns exactly one result with
//     StatusSkip.
func TestJob_EmptyDiscoverReturnsSkip(t *testing.T) {
	t.Parallel()
	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems()).
		WithMapper(workUnit).
		Build()

	results, err := j.Execute(context.Background(), okRunner(t), "/repo")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusSkip,
		"expected single Skip result, got %v",
		results,
	)
}

// TestJob_DiscovererError tests that an error from the discoverer aborts the
// job and is propagated, rather than being swallowed into an empty run.
//
// Why this test is important:
//   - A discovery failure (e.g. no go.work found) means the set of work is
//     unknown, not empty. Treating it as "no items" would let a misconfigured
//     workspace report success while actually testing nothing — a silent,
//     dangerous false pass. The error must surface so the gate fails loudly.
//
// What it tests:
//   - When the discoverer returns an error, Execute returns a non-nil error.
func TestJob_DiscovererError(t *testing.T) {
	t.Parallel()
	disc := interfaces.DiscovererFunc[string](
		func(_ context.Context, _ string) ([]string, error) {
			return nil, apperr.Sentinel("no go.work found")
		},
	)

	j := job.New[string]("lint", "quality").
		WithDiscoverer(disc).
		WithMapper(workUnit).
		Build()

	_, err := j.Execute(context.Background(), okRunner(t), "/repo")
	require.Error(t, err, "expected error from discoverer")
}

// TestJob_WarnOnlyDowngradesFailToWarn tests that a job built with WithWarnOnly
// converts a Fail result into a Warn so it never blocks the enclosing gate.
//
// Why this test is important:
//   - Advisory jobs (e.g. security scanning) are intentionally non-blocking; the
//     WarnOnly downgrade is what lets them report problems without failing CI.
//     If the downgrade did not apply, an advisory failure would block merges,
//     and if it over-applied it would mask genuine blocking failures.
//
// What it tests:
//   - With a runner that always fails and WarnOnly set, Execute returns a single
//     result with StatusWarn (not StatusFail) and no error.
func TestJob_WarnOnlyDowngradesFailToWarn(t *testing.T) {
	t.Parallel()
	// threshold beyond any attempt => the runner always fails.
	failing := countingRunner(t, new(int64), 999, apperr.Sentinel("lint failure"))

	j := job.New[string]("lint", "quality").
		WithDiscoverer(discoverItems("mod")).
		WithMapper(workUnit).
		WithOptions(job.WithWarnOnly()).
		Build()

	results, err := j.Execute(context.Background(), failing, "/repo")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusWarn,
		"expected Warn (WarnOnly mode), got %v",
		results,
	)
}

// TestJob_RetrySucceedsAfterTransientFailure tests that a job configured with
// WithRetry recovers from transient failures and reports Pass once an attempt
// succeeds.
//
// Why this test is important:
//   - Build and network-dependent steps fail intermittently; retry is what keeps
//     CI from flaking on a transient hiccup. The test also pins the attempt
//     count, guarding against an off-by-one where "Retry(2)" runs the wrong
//     number of times (too few defeats the purpose, too many wastes CI time).
//
// What it tests:
//   - With a runner that fails twice then succeeds and WithRetry(2), Execute
//     returns a single Pass result after exactly three attempts (initial + 2
//     retries).
func TestJob_RetrySucceedsAfterTransientFailure(t *testing.T) {
	t.Parallel()
	// Fails on calls 1 and 2; succeeds on call 3.
	var callCount int64
	cr := countingRunner(t, &callCount, 3, apperr.Sentinel("transient"))

	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("mod")).
		WithMapper(workUnit).
		WithOptions(job.WithRetry(2)).
		Build()

	results, err := j.Execute(context.Background(), cr, "/repo")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusPass,
		"expected Pass after retry, got %v",
		results,
	)
	assert.Equal(
		t,
		int64(3),
		atomic.LoadInt64(&callCount),
		"expected 3 attempts (initial + 2 retries)",
	)
}

// TestJob_RetryExhaustedRemainsFailure tests that retry does not paper over a
// persistent failure: once attempts are exhausted the result is still Fail.
//
// Why this test is important:
//   - Retry must improve resilience to flakes without ever converting a real,
//     reproducible failure into a pass. If exhausted retries silently returned
//     success, broken code would sail through the gate.
//
// What it tests:
//   - With a runner that always fails and WithRetry(1), Execute returns no error
//     but a single result with StatusFail.
func TestJob_RetryExhaustedRemainsFailure(t *testing.T) {
	t.Parallel()
	cr := countingRunner(t, new(int64), 999, apperr.Sentinel("persistent"))

	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("mod")).
		WithMapper(workUnit).
		WithOptions(job.WithRetry(1)).
		Build()

	results, err := j.Execute(context.Background(), cr, "/repo")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusFail,
		"expected Fail after retries exhausted, got %v",
		results,
	)
}

// TestJob_Meta tests that a job reports the name and group it was constructed
// with through its Meta accessor.
//
// Why this test is important:
//   - Meta is how phases, observers, and the renderer label a job; a wrong or
//     empty name/group would mislabel progress and summary lines and break job
//     filtering and grouping that key off these fields.
//
// What it tests:
//   - A job built with name "check" and group "quality" reports exactly those
//     values from Meta().
func TestJob_Meta(t *testing.T) {
	t.Parallel()
	j := job.New[string]("check", "quality").
		WithDiscoverer(discoverItems()).
		WithMapper(workUnit).
		Build()

	got := j.Meta()
	assert.Equal(t, "check", got.Name, "Meta() = %+v, want name=check group=quality", got)
	assert.Equal(
		t,
		"quality",
		got.Group,
		"Meta() = %+v, want name=check group=quality",
		got,
	)
}

// --- FilterByLang ---

// TestJob_WithTool_MetaHasTool tests that attaching a ToolSpec via WithTool does
// not disturb the job's identity reported through Meta.
//
// Why this test is important:
//   - WithTool adds a required-tool precondition; that wiring must not corrupt
//     the job's name (the field everything else keys off). A regression here
//     would mislabel tool-backed jobs in the gate output.
//
// What it tests:
//   - A job built with a tool still reports its configured name ("build") from
//     Meta().
func TestJob_WithTool_MetaHasTool(t *testing.T) {
	t.Parallel()
	j := job.New[string]("build", "ci").
		WithTool(goToolSpec(t)).
		WithDiscoverer(discoverItems("pkg/a")).
		WithMapper(workUnit).
		Build()

	meta := j.Meta()
	assert.Equal(t, "build", meta.Name)
}

// TestJob_WithConcurrency tests that constraining a job to a single worker still
// processes every discovered item.
//
// Why this test is important:
//   - Concurrency is a throughput knob, not a correctness one: forcing
//     serialisation (concurrency=1) must yield the same complete result set as
//     the parallel default. Dropping items under a low worker count would mean
//     work is silently skipped on constrained machines or in serialised phases.
//
// What it tests:
//   - With two discovered items and WithConcurrency(1), Execute returns two
//     results.
func TestJob_WithConcurrency(t *testing.T) {
	t.Parallel()
	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("pkg/a", "pkg/b")).
		WithMapper(workUnit).
		WithOptions(job.WithConcurrency(1)).
		Build()

	results, err := j.Execute(context.Background(), okRunner(t), "/repo")
	require.NoError(t, err)
	require.Len(t, results, 2)
}

// TestJob_WithRetryDelay tests that WithRetryDelay composes with WithRetry: a
// delayed retry still recovers a transient failure into a Pass.
//
// Why this test is important:
//   - A retry delay is the backoff that gives a flaky dependency time to
//     recover; the option must wire through without breaking the retry loop or
//     hanging. This guards the combination, not just retry in isolation.
//
// What it tests:
//   - With a runner that fails once then succeeds and WithRetry(1) plus a 1ms
//     WithRetryDelay, Execute returns a single Pass result.
func TestJob_WithRetryDelay(t *testing.T) {
	t.Parallel()
	var callCount int64
	cr := countingRunner(t, &callCount, 2, apperr.Sentinel("transient"))

	j := job.New[string]("build", "ci").
		WithDiscoverer(discoverItems("mod")).
		WithMapper(workUnit).
		WithOptions(job.WithRetry(1), job.WithRetryDelay(time.Millisecond)).
		Build()

	results, err := j.Execute(context.Background(), cr, "/repo")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusPass,
		"expected Pass after retry, got %v",
		results,
	)
}

// TestSingle_ExecutesCommand tests that the Single constructor builds a job that
// runs exactly one command and produces a result.
//
// Why this test is important:
//   - Single is the ergonomic path for the common "run one command" job (most
//     lint/format/typecheck jobs use it); if its discoverer/mapper wiring were
//     wrong the command would never run or would run against the wrong
//     directory, breaking a large class of CLI jobs at once.
//
// What it tests:
//   - A job created via Single executes without error and returns at least one
//     result.
func TestSingle_ExecutesCommand(t *testing.T) {
	t.Parallel()
	j := job.Single(
		"build", "ci", goToolSpec(t),
		func(_ string) []string { return []string{"build", "./..."} },
		func(_ string) string { return "/tmp" },
	)

	results, err := j.Execute(context.Background(), okRunner(t), "/repo")
	require.NoError(t, err)
	require.NotEmpty(t, results, "expected at least 1 result")
}

// TestConditional_SkipsWhenFalse tests that a When-wrapped job whose condition
// is false reports Skip without invoking the inner job.
//
// Why this test is important:
//   - Conditional jobs gate work on context (e.g. only run a migration check
//     when schema files changed). If a false condition still ran the inner job,
//     the conditional would be pointless and could trigger expensive or
//     destructive work that was meant to be skipped.
//
// What it tests:
//   - With a condition returning false, Execute returns a single Skip result,
//     and Meta still reports the inner job's name.
func TestConditional_SkipsWhenFalse(t *testing.T) {
	t.Parallel()
	c := job.When(
		passJob(t, "lint"),
		func(_ context.Context, _ string) bool { return false },
	)

	assert.Equal(t, "lint", c.Meta().Name)
	results, err := c.Execute(context.Background(), okRunner(t), "/root")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusSkip,
		"expected Skip when condition=false, got %v",
		results,
	)
}

// TestConditional_RunsWhenTrue tests that a When-wrapped job whose condition is
// true delegates to the inner job and returns its result.
//
// Why this test is important:
//   - This is the complement to the skip case: when the gating condition holds,
//     the conditional must be transparent and actually run the work. A
//     conditional that skipped even when true would silently drop required
//     checks.
//
// What it tests:
//   - With a condition returning true, Execute returns the inner job's single
//     Pass result.
func TestConditional_RunsWhenTrue(t *testing.T) {
	t.Parallel()
	c := job.When(
		passJob(t, "lint"),
		func(_ context.Context, _ string) bool { return true },
	)

	results, err := c.Execute(context.Background(), okRunner(t), "/root")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusPass,
		"expected Pass when condition=true, got %v",
		results,
	)
}

// TestFilterByLang tests that FilterByLang selects jobs by enabled language
// flags while always including infra jobs.
//
// Why this test is important:
//   - CI runs only the language legs a PR actually touches to save time, but
//     shared infrastructure (Docker, k8s, proto) must be validated on every
//     change. Getting this wrong would either run irrelevant language jobs
//     (wasting CI) or, worse, skip infra validation on a change that breaks it.
//
// What it tests:
//   - Each single-language flag selects that language's job plus the infra job
//     (count 2, language job first); all flags select all four; no flags still
//     yields just the infra job.
func TestFilterByLang(t *testing.T) {
	t.Parallel()
	tagged := []job.TaggedJob{
		job.Tag(job.LangGo, passJob(t, "go-lint")),
		job.Tag(job.LangPython, passJob(t, "py-lint")),
		job.Tag(job.LangTS, passJob(t, "ts-check")),
		job.Tag(job.LangInfra, passJob(t, "proto-lint")),
	}

	cases := []struct {
		wantFirst string
		wantCount int
		go_       bool
		py        bool
		ts        bool
	}{
		{go_: true, wantCount: 2, wantFirst: "go-lint"},                     // go + infra
		{py: true, wantCount: 2, wantFirst: "py-lint"},                      // py + infra
		{ts: true, wantCount: 2, wantFirst: "ts-check"},                     // ts + infra
		{go_: true, py: true, ts: true, wantCount: 4, wantFirst: "go-lint"}, // all four
		{
			wantCount: 1,
			wantFirst: "proto-lint",
		}, // infra always included
	}

	for _, tc := range cases {
		got := job.FilterByLang(tagged, tc.go_, tc.py, tc.ts)
		if !assert.Len(
			t,
			got,
			tc.wantCount,
			"FilterByLang(%v,%v,%v)",
			tc.go_,
			tc.py,
			tc.ts,
		) {
			continue
		}
		assert.Equal(t, tc.wantFirst, got[0].Meta().Name, "FilterByLang first job")
	}
}
