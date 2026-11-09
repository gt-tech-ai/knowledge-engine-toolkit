package unit_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/report"
)

// reportSteps is a fixed phase: one pass, one failure with detail.
func reportSteps() types.StepResults {
	return types.StepResults{
		{
			Name:     textGofmt,
			Group:    textLint,
			Status:   types.StatusPass,
			Duration: durationSteps * time.Millisecond,
		},
		{
			Name:     textVet,
			Group:    textLint,
			Status:   types.StatusFail,
			Duration: duration * time.Second,
			Error:    "1 finding",
			Detail:   []byte("a.go:3: shadowed\n"),
		},
	}
}

// drive sends one gate with one phase of reportSteps through obs.
func drive(obs interfaces.ExecutionObserver) {
	steps := reportSteps()

	obs.OnGateStart(textPreflight, []string{textLint})
	obs.OnPhaseStart(textLint, []string{textGofmt, textVet})

	for _, s := range steps {
		obs.OnStepStart(s.Name, s.Group)
		obs.OnStepComplete(&s)
	}

	obs.OnPhaseComplete(textLint, steps, 3*time.Second)
	obs.OnGateComplete(textPreflight, steps, 4*time.Second)
}

// reportOf builds kind over fresh buffers and drives it.
func reportOf(t *testing.T, cfg report.Config) (out, errOut string) {
	t.Helper()

	var buffer, currentBuffer bytes.Buffer

	obs, err := report.NewFromConfig(
		cfg,
		types.Streams{Out: &buffer, Err: &currentBuffer},
	)
	require.NoError(t, err)
	drive(obs)

	return buffer.String(), currentBuffer.String()
}

// TestFoundationReport_LineAndTable tests the terminal reporters NewFromConfig builds.
//
// Why this test is important:
//   - These are what a developer reads after every gate: which phases failed, which
//     steps and why, the hint that fixes it, and the totals, on the diagnostics stream.
//     Interactive must be what turns the per-step ticks on, so CI logs stay one line
//     per phase.
//
// What it tests:
//   - line prints the phase status line, the failure block with the step's actionable
//     output and its hint, and the summary on Err; Interactive adds the gate header,
//     the running list and a tick per step; table prints the phase and final tables
//     with their TOTAL lines; none writes to Out or emits ANSI without Color.
func TestFoundationReport_LineAndTable(t *testing.T) {
	t.Parallel()

	hints := []report.Hint{{Group: textLint, Substr: textVet, Text: "Run: go vet ./..."}}
	failureBlock := "  Failures:\n\n" +
		"    FAIL vet\n" +
		"      a.go:3: shadowed\n" +
		"      Run: go vet ./...\n\n"

	out, errOut := reportOf(t, report.Config{Kind: report.KindLine, Hints: hints})
	assert.Empty(t, out)
	assert.Equal(t, "==> lint                FAIL  3s\n"+
		failureBlock+
		"\n1 ok, 1 failed  (4s)\n", errOut)

	out, errOut = reportOf(
		t,
		report.Config{Kind: report.KindLine, Hints: hints, Interactive: true},
	)
	assert.Empty(t, out)
	assert.Equal(t, "==> preflight\n"+
		"  running: gofmt, vet\n"+
		"  ✓ gofmt  (1.5s)\n"+
		"  ✗ vet  (2s)\n"+
		"==> lint                FAIL  3s\n"+
		failureBlock+
		"\n1 ok, 1 failed  (4s)\n", errOut)

	out, errOut = reportOf(t, report.Config{Kind: report.KindTable})
	assert.Empty(t, out)
	assert.Contains(t, errOut, "\n-- lint --\n")
	assert.Contains(
		t,
		errOut,
		"    vet                          FAIL             2s  1 finding\n",
	)
	assert.Contains(t, errOut, "  TOTAL: 1 passed, 1 failed (3s)\n")
	assert.Contains(t, errOut, "\n-- preflight - Final Results --\n")
	assert.Contains(t, errOut, "  TOTAL: 1 passed, 1 failed (4s)\n")
	assert.NotContains(t, errOut, "\x1b[")
}

// TestFoundationReport_MachineKinds tests the CI and JSON reporters and the rest.
//
// Why this test is important:
//   - CI turns ::error lines into annotations on the PR, and scripts read the JSON
//     report; a malformed line or field breaks both silently.
//
// What it tests:
//   - gh-actions writes a log group, an error annotation per failure and the gate
//     verdict on Out; json writes one parseable document with every step and the
//     counts; quiet writes nothing; Composite forwards to each observer; an unknown
//     kind and missing streams are INVALID_INPUT.
func TestFoundationReport_MachineKinds(t *testing.T) {
	t.Parallel()

	out, errOut := reportOf(t, report.Config{Kind: report.KindGHActions})
	assert.Empty(t, errOut)
	assert.Equal(t, "::group::lint\n  PASS  gofmt\n  FAIL  vet\n::endgroup::\n"+
		"::error title=vet::1 finding\n::error title=Gate::1 step(s) failed\n", out)

	out, _ = reportOf(t, report.Config{Kind: report.KindJSON})

	// shapeSteps is the Steps element of this document.
	type shapeSteps struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		DurationMS int64  `json:"duration_ms"`
	}

	var doc struct {
		Counts    map[string]int `json:"counts"`
		Gate      string         `json:"gate"`
		Steps     []shapeSteps   `json:"steps"`
		ElapsedMS int64          `json:"elapsed_ms"`
		Passed    bool           `json:"passed"`
	}

	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, textPreflight, doc.Gate)
	assert.False(t, doc.Passed)
	assert.Equal(t, int64(wantElapsedMS), doc.ElapsedMS)
	assert.Equal(t, map[string]int{"PASS": 1, "FAIL": 1}, doc.Counts)
	require.Len(t, doc.Steps, wantStepsLen)
	assert.Equal(t, "FAIL", doc.Steps[1].Status)
	assert.Equal(t, int64(wantDurationMS), doc.Steps[0].DurationMS)

	out, errOut = reportOf(t, report.Config{})
	assert.Empty(t, out+errOut)

	var currentBuffer, buffer bytes.Buffer

	executionObserver, err := report.NewFromConfig(
		report.Config{Kind: report.KindGHActions},
		types.Streams{Out: &currentBuffer, Err: &currentBuffer},
	)
	require.NoError(t, err)

	js, err := report.NewFromConfig(report.Config{Kind: report.KindJSON},
		types.Streams{Out: &buffer, Err: &buffer})
	require.NoError(t, err)
	drive(report.Composite(executionObserver, js))
	assert.Contains(t, currentBuffer.String(), "::group::lint")
	assert.Contains(t, buffer.String(), `"gate": "preflight"`)

	_, err = report.NewFromConfig(
		report.Config{Kind: "html"},
		types.Streams{Out: &currentBuffer, Err: &currentBuffer},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = report.NewFromConfig(report.Config{}, types.Streams{Out: &currentBuffer})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestFoundationReport_GHActionsEscapes tests the escaping of annotation values.
//
// Why this test is important:
//   - GitHub reads `::` as the end of a command's properties and `,` as the start of the
//     next property, so an unescaped step name cuts its own title short; a raw newline
//     ends the annotation's message at its first line.
//
// What it tests:
//   - A failing step named `lint:go,x` gets the title `lint%3Ago%2Cx`; its message keeps
//     `:` and `,` and encodes `%` and the newline.
func TestFoundationReport_GHActionsEscapes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	obs, err := report.NewFromConfig(report.Config{Kind: report.KindGHActions},
		types.Streams{Out: &out, Err: &out})
	require.NoError(t, err)

	step := types.StepResult{
		Name: "lint:go,x", Group: textLint, Status: types.StatusFail,
		Error: "a.go:3: 100% bad,\nsee above",
	}
	obs.OnPhaseStart(textLint, []string{step.Name})
	obs.OnStepComplete(&step)
	obs.OnPhaseComplete(textLint, types.StepResults{step}, time.Second)
	assert.Contains(t, out.String(),
		"::error title=lint%3Ago%2Cx::a.go:3: 100%25 bad,%0Asee above\n")
}

const (
	// durationSteps is the duration steps the tests use.
	durationSteps = 1500

	// wantDurationMS is the expected duration ms.
	wantDurationMS = 1500

	// wantElapsedMS is the expected elapsed ms.
	wantElapsedMS = 4000
)
