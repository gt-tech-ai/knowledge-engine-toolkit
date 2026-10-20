package unit_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/report"
)

// reportSteps is a fixed phase: one pass, one failure with detail.
func reportSteps() types.StepResults {
	return types.StepResults{
		{
			Name:     "gofmt",
			Group:    "lint",
			Status:   types.StatusPass,
			Duration: 1500 * time.Millisecond,
		},
		{
			Name:     "vet",
			Group:    "lint",
			Status:   types.StatusFail,
			Duration: 2 * time.Second,
			Error:    "1 finding",
			Detail:   []byte("a.go:3: shadowed\n"),
		},
	}
}

// drive sends one gate with one phase of reportSteps through obs.
func drive(obs interfaces.ExecutionObserver) {
	steps := reportSteps()
	obs.OnGateStart("preflight", []string{"lint"})
	obs.OnPhaseStart("lint", []string{"gofmt", "vet"})
	for _, s := range steps {
		obs.OnStepStart(s.Name, s.Group)
		obs.OnStepComplete(s)
	}
	obs.OnPhaseComplete("lint", steps, 3*time.Second)
	obs.OnGateComplete("preflight", steps, 4*time.Second)
}

// reportOf builds kind over fresh buffers and drives it.
func reportOf(t *testing.T, cfg report.Config) (out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	obs, err := report.NewFromConfig(cfg, types.Streams{Out: &o, Err: &e})
	require.NoError(t, err)
	drive(obs)
	return o.String(), e.String()
}

// TestFoundationReport_LineAndTable tests the terminal reporters.
//
// Why this test is important:
//   - These are what a developer reads after every gate: each step's time, which steps
//     failed and why, and the totals must be on screen, on the diagnostics stream.
//
// What it tests:
//   - line ticks each step with its duration, prints the phase status, the failure
//     with its indented detail and the summary on Err; table prints the phase and
//     final tables with the failure; neither writes to Out.
func TestFoundationReport_LineAndTable(t *testing.T) {
	t.Parallel()
	out, errOut := reportOf(t, report.Config{Kind: report.KindLine})
	assert.Empty(t, out)
	assert.Equal(t, "==> preflight\n"+
		"  running: gofmt, vet\n"+
		"  ✓ gofmt  (1.5s)\n"+
		"  ✗ vet  (2s)\n"+
		"==> lint               FAIL  3s\n"+
		"\n✗ vet: 1 finding\n"+
		"    a.go:3: shadowed\n"+
		"\n1 ok, 1 failed (4s)\n", errOut)

	out, errOut = reportOf(t, report.Config{Kind: report.KindTable})
	assert.Empty(t, out)
	assert.Equal(t, "\nlint\n"+
		"STATUS  STEP   GROUP  TIME  ERROR\n"+
		"PASS    gofmt  lint   1.5s  \n"+
		"FAIL    vet    lint   2s    1 finding\n"+
		"1 ok, 1 failed (3s)\n"+
		"\npreflight - Final Results\n"+
		"STATUS  STEP   GROUP  TIME  ERROR\n"+
		"PASS    gofmt  lint   1.5s  \n"+
		"FAIL    vet    lint   2s    1 finding\n"+
		"1 ok, 1 failed (4s)\n"+
		"\n✗ vet: 1 finding\n"+
		"    a.go:3: shadowed\n", errOut)
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
	var doc struct {
		Counts map[string]int `json:"counts"`
		Gate   string         `json:"gate"`
		Steps  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			DurationMS int64  `json:"duration_ms"`
		} `json:"steps"`
		ElapsedMS int64 `json:"elapsed_ms"`
		Passed    bool  `json:"passed"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "preflight", doc.Gate)
	assert.False(t, doc.Passed)
	assert.Equal(t, int64(4000), doc.ElapsedMS)
	assert.Equal(t, map[string]int{"PASS": 1, "FAIL": 1}, doc.Counts)
	require.Len(t, doc.Steps, 2)
	assert.Equal(t, "FAIL", doc.Steps[1].Status)
	assert.Equal(t, int64(1500), doc.Steps[0].DurationMS)

	out, errOut = reportOf(t, report.Config{})
	assert.Empty(t, out+errOut)

	var a, b bytes.Buffer
	gh, err := report.NewFromConfig(report.Config{Kind: report.KindGHActions},
		types.Streams{Out: &a, Err: &a})
	require.NoError(t, err)
	js, err := report.NewFromConfig(report.Config{Kind: report.KindJSON},
		types.Streams{Out: &b, Err: &b})
	require.NoError(t, err)
	drive(report.Composite(gh, js))
	assert.Contains(t, a.String(), "::group::lint")
	assert.Contains(t, b.String(), `"gate": "preflight"`)

	_, err = report.NewFromConfig(
		report.Config{Kind: "html"},
		types.Streams{Out: &a, Err: &a},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = report.NewFromConfig(report.Config{}, types.Streams{Out: &a})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
