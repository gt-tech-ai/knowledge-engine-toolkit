package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/resmetrics"
)

// resmetricsOverBound is one value past the taxonomy's 8-value enum bound.
const resmetricsOverBound = 9

// resmetricsTaxonomy is a small valid taxonomy: one counter with an enum label, one
// enum-valued gauge and one plain gauge.
const resmetricsTaxonomy = `enums:
  - name: reason
    go: Reason
    doc: Why a call was refused.
    values:
      - {name: rate_limited, doc: No token.}
  - name: breaker_state
    go: BreakerState
    doc: A breaker's state.
    values:
      - {name: closed, gauge: 0, doc: Calls pass.}
      - {name: open, gauge: 2, doc: Calls fail fast.}
series:
  - name: resilience_rejections_total
    kind: counter
    method: Rejected
    help: Calls refused before they ran.
    labels:
      - {name: operation}
      - {name: reason, enum: reason}
  - name: resilience_breaker_state
    kind: gauge
    method: SetBreakerState
    help: A breaker's state.
    value: breaker_state
  - name: resilience_bulkhead_limit
    kind: gauge
    method: SetBulkheadLimit
    help: A bulkhead's limit.
`

// resmetricsGenerator returns the resilience metric generator under the shared drift
// checks.
func resmetricsGenerator() taxonomyGenerator {
	return taxonomyGenerator{
		newGenerator: resmetrics.New,
		source:       resmetrics.SourcePath,
		goPath:       resmetrics.GoPath,
		pyPath:       resmetrics.PythonPath,
		regenerate:   "go generate ./go/foundation/resilience/series/...",
	}
}

// TestResmetrics_RendersRecorderMethods tests that the generator renders a typed
// recorder method per series in both languages.
//
// Why this test is important:
//   - the recorder is the only way code emits a resilience series; a method that binds
//     the wrong labels, or a gauge that sets a string, would export series no dashboard
//     can query.
//
// What it tests:
//   - Go: the counter's method takes the operation and a typed Reason and registers the
//     tier and name labels first; the enum-valued gauge sets the enum's gauge number;
//     the plain gauge takes a float64.
//   - Python: the same methods in snake_case, with keyword label values.
func TestResmetrics_RendersRecorderMethods(t *testing.T) {
	t.Parallel()

	cb := memoryCodebase(t, map[string]string{resmetrics.SourcePath: resmetricsTaxonomy})

	out, err := resmetrics.New().Render(t.Context(), cb)
	require.NoError(t, err)

	goSrc, pySrc := string(out[resmetrics.GoPath]), string(out[resmetrics.PythonPath])

	for _, want := range []string{
		`ReasonRateLimited Reason = "rate_limited"`,
		`"resilience_rejections_total",`,
		`"tier", "name", "operation", "reason"`,
		"func (r *Recorder) Rejected(operation string, reason Reason) {",
		"r.rejections.Inc(r.tier, r.name, operation, string(reason))",
		"func (r *Recorder) SetBreakerState(value BreakerState) {",
		"r.breakerState.Set(value.Gauge(), r.tier, r.name)",
		"case BreakerStateOpen:\n\t\treturn 2",
		"func (r *Recorder) SetBulkheadLimit(value float64) {",
	} {
		require.Contains(t, goSrc, want)
	}

	for _, want := range []string{
		`RATE_LIMITED = "rate_limited"`,
		`["tier", "name", "operation", "reason"]`,
		"def rejected(self, operation: str, reason: Reason) -> None:",
		"operation=operation,\n                reason=reason.value,",
		"def set_breaker_state(self, value: BreakerState) -> None:",
		"def set_bulkhead_limit(self, value: float) -> None:",
	} {
		require.Contains(t, pySrc, want)
	}
}

// TestResmetrics_CommittedOutputsMatchTaxonomy tests that the committed Go and Python
// series files are a fresh render of the taxonomy.
//
// Why this test is important:
//   - the taxonomy is the one source of every resilience series name, label and enum
//     value; a hand-edited output, or a taxonomy edit that was not regenerated, would
//     let Go and Python export different series for the same event.
//
// What it tests:
//   - the generator renders exactly the Go and Python outputs, and gen.Check over the
//     repository reports both in sync.
func TestResmetrics_CommittedOutputsMatchTaxonomy(t *testing.T) {
	t.Parallel()

	resmetricsGenerator().requireInSync(t)
}

// TestResmetrics_HandEditIsDrift tests that editing a generated output by hand is
// drift.
//
// Why this test is important:
//   - a series renamed in one language's output only is exactly the cross-language
//     mismatch the generator exists to prevent; the drift check must name the file.
//
// What it tests:
//   - a series name edited in the Go output, or an enum value added to the Python
//     output, makes gen.Check report that one path out of sync.
func TestResmetrics_HandEditIsDrift(t *testing.T) {
	t.Parallel()

	resmetricsGenerator().requireHandEditsDrift(t, []handEdit{
		{
			name: "go series", path: resmetrics.GoPath,
			from: `"resilience_retries_total",`,
			to:   `"resilience_retry_total",`,
		},
		{
			name: "python value", path: resmetrics.PythonPath,
			from: `    RATE_LIMITED = "rate_limited"`,
			to:   "    RATE_LIMITED = \"rate_limited\"\n    QUOTA = \"quota\"",
		},
	})
}

// rowEnd closes one flow-style series row in a test taxonomy.
const rowEnd = "}\n"

// TestResmetrics_RejectsInvalidTaxonomy tests that a malformed taxonomy fails to render.
//
// Why this test is important:
//   - a mistyped kind, label or enum would otherwise render a series that panics at
//     registration, or one whose labels grow without bound.
//
// What it tests:
//   - each rule parse enforces (prefix, _total suffixes, unique names and methods, known
//     labels and enums, gauge numbers on all of an enum's values or none, a gauge with
//     no labels, the 8-value enum bound, known fields) fails Render with
//     CodeInvalidInput.
func TestResmetrics_RejectsInvalidTaxonomy(t *testing.T) {
	t.Parallel()

	enums := "enums:\n  - name: reason\n    go: Reason\n    doc: d.\n" +
		"    values:\n      - {name: a, doc: d.}\n"
	row := func(fields string) string {
		return enums + "series:\n  - {" + fields + rowEnd
	}
	counter := "name: resilience_x_total, kind: counter, method: X, help: h."
	overBound := "enums:\n  - name: reason\n    go: Reason\n    doc: d.\n    values:\n" +
		strings.Repeat("      - {name: a, doc: d.}\n", resmetricsOverBound) +
		"series:\n  - {" + counter + rowEnd

	cases := map[string]string{
		"prefix":  row("name: other_total, kind: counter, method: X, help: h."),
		"counter": row("name: resilience_x, kind: counter, method: X, help: h."),
		"gauge":   row("name: resilience_x_total, kind: gauge, method: X, help: h."),
		"kind":    row("name: resilience_x_total, kind: summary, method: X, help: h."),
		"help":    row("name: resilience_x_total, kind: counter, method: X"),
		"label":   row(counter + ", labels: [{name: colour}]"),
		"enum":    row(counter + ", labels: [{name: reason, enum: why}]"),
		"value": row(
			"name: resilience_x, kind: gauge, method: X, help: h., value: reason",
		),
		"bound": overBound,
		"gauge labels": row(
			"name: resilience_x, kind: gauge, method: X, help: h., " +
				"labels: [{name: operation}]",
		),
		"partial gauge": "enums:\n  - name: reason\n    go: Reason\n    doc: d.\n" +
			"    values:\n      - {name: a, doc: d., gauge: 0}\n" +
			"      - {name: b, doc: d.}\nseries:\n  - {" + counter + rowEnd,
		"field": row(counter + ", colour: red"),
		"twice": row(counter) +
			"  - {name: resilience_x_total, kind: counter, method: Y, help: h.}\n",
		"method": row(counter) +
			"  - {name: resilience_y_total, kind: counter, method: X, help: h.}\n",
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cb := memoryCodebase(t, map[string]string{resmetrics.SourcePath: src})

			_, err := resmetrics.New().Render(t.Context(), cb)

			require.Error(t, err)
			require.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
		})
	}
}

// declaredSeries is the part of the taxonomy the docs check reads: the series names.
type declaredSeries struct {
	// Series are the taxonomy's rows.
	Series []declaredRow `yaml:"series"`
}

// declaredRow is one taxonomy row's name.
type declaredRow struct {
	// Name is the series name.
	Name string `yaml:"name"`
}

// TestResmetrics_EverySeriesIsDocumented tests that ARCHITECTURE.md's resilience
// metrics table names every series the taxonomy declares.
//
// Why this test is important:
//   - operators and agents read the series from ARCHITECTURE.md; a series added to the
//     taxonomy without its row is a metric nobody knows to query.
//
// What it tests:
//   - every series name in taxonomy/resilience-metrics.yaml appears in ARCHITECTURE.md
//     in backquotes.
func TestResmetrics_EverySeriesIsDocumented(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(
		filepath.FromSlash(repoRoot + textSlash + resmetrics.SourcePath),
	)
	require.NoError(t, err)

	var declared declaredSeries

	require.NoError(t, yaml.Unmarshal(src, &declared))
	require.NotEmpty(t, declared.Series)

	doc, err := os.ReadFile(filepath.FromSlash(repoRoot + "/ARCHITECTURE.md"))
	require.NoError(t, err)

	for _, row := range declared.Series {
		require.Contains(t, string(doc), "`"+row.Name+"`", "ARCHITECTURE.md lacks a row")
	}
}
