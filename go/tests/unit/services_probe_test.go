package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/probe"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// probeOf returns a generated Probe mock named name whose runs return verdicts in order,
// the last one repeating.
func probeOf(
	ctrl *gomock.Controller,
	name string,
	verdicts ...types.Verdict,
) *mocks.MockProbe[string] {
	p := mocks.NewMockProbe[string](ctrl)
	p.EXPECT().Name().Return(name).AnyTimes()
	calls := 0
	p.EXPECT().Run(gomock.Any(), "target").DoAndReturn(
		func(context.Context, string) types.Verdict {
			v := verdicts[min(calls, len(verdicts)-1)]
			calls++
			return v
		}).AnyTimes()
	return p
}

// newSet builds a probe set over probes with opts.
func newSet(
	t *testing.T,
	opts probe.Options,
	probes ...interfaces.Probe[string],
) interfaces.ProbeSet[string] {
	t.Helper()
	set, err := probe.NewSet(fixtures.Decorations(), "guards", probes, opts)
	require.NoError(t, err)
	return set
}

// TestServicesProbe_OneStepPerProbeInOrder tests the result shape of a probe set.
//
// Why this test is important:
//   - Every check front-end (guards, smoke, doctor, manifest checks) renders a set's
//     steps; results in completion order would reorder the report on every run, and a
//     failing probe must say what it found.
//
// What it tests:
//   - Probes run in parallel but their steps come back in probe order, named by the
//     probe, grouped by the set; a failing verdict's findings are counted in Error and
//     listed in Detail; a warning keeps its status.
func TestServicesProbe_OneStepPerProbeInOrder(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	set := newSet(
		t,
		probe.Options{Parallel: 3},
		probeOf(ctrl, "a", types.Verdict{Status: types.StatusPass}),
		probeOf(
			ctrl,
			"b",
			types.Verdict{Status: types.StatusFail, Findings: []types.Finding{
				{
					Check:   "b",
					File:    "x.go",
					Line:    3,
					Rule:    "no-todo",
					Message: "TODO left",
				},
				{Check: "b", Message: "repo-wide problem"},
			}},
		),
		probeOf(ctrl, "c", types.Verdict{Status: types.StatusWarn, Detail: "slow"}),
	)
	res, err := set.Run(t.Context(), "target")
	require.NoError(t, err)
	require.Len(t, res, 3)
	assert.Equal(
		t,
		[]string{"a", "b", "c"},
		[]string{res[0].Name, res[1].Name, res[2].Name},
	)
	assert.Equal(t, "guards", res[1].Group)
	assert.Equal(t, types.StatusPass, res[0].Status)
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Equal(t, "2 findings", res[1].Error)
	assert.Equal(
		t,
		"x.go:3: no-todo: TODO left\nrepo-wide problem\n",
		string(res[1].Detail),
	)
	assert.Equal(t, types.StatusWarn, res[2].Status)
	assert.Equal(t, "slow", res[2].Error)
}

// TestServicesProbe_Strict tests strict mode.
//
// Why this test is important:
//   - CI must not pass a check that could not run (a missing tool, an unreachable
//     target); locally a skip is fine. Strict is how CI says so.
//
// What it tests:
//   - A skipped probe is a skip by default and a failure under Strict, keeping the
//     probe's detail.
func TestServicesProbe_Strict(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	skip := types.Verdict{Status: types.StatusSkip, Detail: "helm not installed"}

	res, err := newSet(
		t,
		probe.Options{},
		probeOf(ctrl, "s", skip),
	).Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)

	res, err = newSet(t, probe.Options{Strict: true}, probeOf(ctrl, "s", skip)).
		Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "skipped under strict: helm not installed", res[0].Error)
}

// TestServicesProbe_Timeout tests the per-probe deadline.
//
// Why this test is important:
//   - A live probe against a hung endpoint must not stall the whole set, even when the
//     probe ignores its context.
//
// What it tests:
//   - A probe that outlives Timeout is a failed step whose error carries TIMEOUT, and
//     the other probes' steps are unaffected.
func TestServicesProbe_Timeout(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := mocks.NewMockProbe[string](ctrl)
	hung.EXPECT().Name().Return("hung").AnyTimes()
	hung.EXPECT().Run(gomock.Any(), "target").DoAndReturn(
		func(context.Context, string) types.Verdict {
			<-release
			return types.Verdict{Status: types.StatusPass}
		})
	set := newSet(t, probe.Options{Timeout: 50 * time.Millisecond},
		hung, probeOf(ctrl, "ok", types.Verdict{Status: types.StatusPass}))
	res, err := set.Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Contains(t, res[0].Error, string(apperr.CodeTimeout))
	assert.Equal(t, types.StatusPass, res[1].Status)
}

// TestServicesProbe_Retry tests retries of transient failures.
//
// Why this test is important:
//   - A live target that is still starting fails once and then passes; retrying a
//     deterministic failure only wastes time and hides nothing.
//
// What it tests:
//   - A transient failure is re-run up to Retry times and passes when a retry passes;
//     a failure still transient after every retry fails; a non-transient failure runs
//     once.
func TestServicesProbe_Retry(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	flaky := types.Verdict{Status: types.StatusFail, Transient: true, Detail: "not ready"}
	pass := types.Verdict{Status: types.StatusPass}

	res, err := newSet(t, probe.Options{Retry: 2}, probeOf(ctrl, "f", flaky, pass)).
		Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, res[0].Status)

	res, err = newSet(
		t,
		probe.Options{Retry: 2},
		probeOf(ctrl, "f", flaky, flaky, flaky, pass),
	).
		Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	once := mocks.NewMockProbe[string](ctrl)
	once.EXPECT().Name().Return("once").AnyTimes()
	once.EXPECT().Run(gomock.Any(), "target").
		Return(types.Verdict{Status: types.StatusFail, Detail: "bad"}).Times(1)
	res, err = newSet(t, probe.Options{Retry: 3}, once).Run(t.Context(), "target")
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)
}

// TestServicesProbe_CancelledAndInvalid tests a cancelled run and invalid options.
//
// Why this test is important:
//   - A cancelled run must say so, not report a clean set; a negative option is a
//     configuration mistake that must fail at construction.
//
// What it tests:
//   - Run on a cancelled context returns CANCELED; a negative Retry or Timeout is
//     INVALID_INPUT from NewSet.
func TestServicesProbe_CancelledAndInvalid(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newSet(
		t,
		probe.Options{},
		probeOf(ctrl, "a", types.Verdict{}),
	).Run(ctx, "target")
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))

	_, err = probe.NewSet[string](
		fixtures.Decorations(),
		"g",
		nil,
		probe.Options{Retry: -1},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = probe.NewSet[string](
		fixtures.Decorations(),
		"g",
		nil,
		probe.Options{Timeout: -1},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
