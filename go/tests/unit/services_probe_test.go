package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/probe"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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
	currentProbe := mocks.NewMockProbe[string](ctrl)
	currentProbe.EXPECT().Name().Return(name).AnyTimes()

	calls := 0

	currentProbe.EXPECT().Run(gomock.Any(), textTarget).DoAndReturn(
		func(context.Context, string) types.Verdict {
			v := verdicts[min(calls, len(verdicts)-1)]
			calls++

			return v
		}).AnyTimes()

	return currentProbe
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
		fixtures.Build(func(row *probe.Options) {
			row.Parallel = 3
		}),
		probeOf(
			ctrl,
			textA,
			fixtures.Build(func(row *types.Verdict) {
				row.Status = types.StatusPass
			}),
		),
		probeOf(
			ctrl,
			textB,
			types.Verdict{
				Status: types.StatusFail, Findings: []types.Finding{
					{
						Check:   textB,
						File:    "x.go",
						Line:    3,
						Rule:    "no-todo",
						Message: "TODO left",
					},
					fixtures.Build(func(row *types.Finding) {
						row.Check = textB
						row.Message = "repo-wide problem"
					}),
				},
				Detail:    "",
				Transient: false,
			},
		),
		probeOf(
			ctrl,
			textC,
			types.Verdict{
				Status:    types.StatusWarn,
				Detail:    textSlow,
				Findings:  nil,
				Transient: false,
			},
		),
	)

	testServicesProbeOneStepPerProbeInOrderChecksSteps(t, set)
}

// testServicesProbeOneStepPerProbeInOrderChecksSteps continues
// TestServicesProbe_OneStepPerProbeInOrder.
func testServicesProbeOneStepPerProbeInOrderChecksSteps(
	t *testing.T,
	set interfaces.ProbeSet[string],
) {
	t.Helper()

	res, err := set.Run(t.Context(), textTarget)
	require.NoError(t, err)
	require.Len(t, res, wantResLen)
	assert.Equal(
		t,
		[]string{textA, textB, textC},
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
	assert.Equal(t, textSlow, res[2].Error)
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
	skip := types.Verdict{
		Status:    types.StatusSkip,
		Detail:    "helm not installed",
		Findings:  nil,
		Transient: false,
	}

	res, err := newSet(
		t,
		stdx.Zero[probe.Options](),
		probeOf(ctrl, textS, skip),
	).Run(t.Context(), textTarget)
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)

	res, err = newSet(
		t,
		fixtures.Build(func(row *probe.Options) {
			row.Strict = true
		}),
		probeOf(ctrl, textS, skip),
	).
		Run(t.Context(), textTarget)
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
	hung.EXPECT().Run(gomock.Any(), textTarget).DoAndReturn(
		func(context.Context, string) types.Verdict {
			<-release

			return fixtures.Build(func(row *types.Verdict) {
				row.Status = types.StatusPass
			})
		})

	set := newSet(
		t,
		fixtures.Build(func(row *probe.Options) {
			row.Timeout = 50 * time.Millisecond
		}),
		hung,
		probeOf(
			ctrl,
			textOk,
			fixtures.Build(func(row *types.Verdict) {
				row.Status = types.StatusPass
			}),
		),
	)
	res, err := set.Run(t.Context(), textTarget)
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
	flaky := types.Verdict{
		Status:    types.StatusFail,
		Transient: true,
		Detail:    "not ready",
		Findings:  nil,
	}
	pass := fixtures.Build(func(row *types.Verdict) {
		row.Status = types.StatusPass
	})

	res, err := newSet(
		t,
		fixtures.Build(func(row *probe.Options) {
			row.Retry = 2
		}),
		probeOf(ctrl, textF, flaky, pass),
	).
		Run(t.Context(), textTarget)
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, res[0].Status)

	res, err = newSet(
		t,
		fixtures.Build(func(row *probe.Options) {
			row.Retry = 2
		}),
		probeOf(ctrl, textF, flaky, flaky, flaky, pass),
	).
		Run(t.Context(), textTarget)
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	once := mocks.NewMockProbe[string](ctrl)
	once.EXPECT().Name().Return(textOnce).AnyTimes()
	once.EXPECT().Run(gomock.Any(), textTarget).
		Return(types.Verdict{
			Status: types.StatusFail, Detail: textBad, Findings: nil,
			Transient: false,
		}).
		Times(1)

	res, err = newSet(
		t,
		fixtures.Build(func(row *probe.Options) {
			row.Retry = 3
		}),
		once,
	).Run(t.Context(), textTarget)
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
		stdx.Zero[probe.Options](),
		probeOf(ctrl, textA, stdx.Zero[types.Verdict]()),
	).Run(ctx, textTarget)
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))

	_, err = probe.NewSet[string](
		fixtures.Decorations(),
		"g",
		nil,
		fixtures.Build(func(row *probe.Options) {
			row.Retry = -1
		}),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = probe.NewSet[string](
		fixtures.Decorations(),
		"g",
		nil,
		fixtures.Build(func(row *probe.Options) {
			row.Timeout = -1
		}),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestServicesProbe_PanickingProbe tests a probe that panics.
//
// Why this test is important:
//   - Probes run on worker goroutines, where a panic is not caught by the service's
//     recovery decorator; one buggy check would crash the whole run and hide every
//     other probe's result.
//
// What it tests:
//   - With and without a per-probe timeout, a panicking probe is a failed step with one
//     finding naming the panic and the stack in its detail, and the other probes'
//     steps are unaffected.
func TestServicesProbe_PanickingProbe(t *testing.T) {
	t.Parallel()

	for _, opts := range []probe.Options{
		stdx.Zero[probe.Options](),
		fixtures.Build(func(row *probe.Options) {
			row.Timeout = time.Minute
		}),
	} {
		ctrl := gomock.NewController(t)
		bad := mocks.NewMockProbe[string](ctrl)
		bad.EXPECT().Name().Return(textBad).AnyTimes()
		bad.EXPECT().Run(gomock.Any(), textTarget).DoAndReturn(
			func(context.Context, string) types.Verdict { panic("nil map") })

		res, err := newSet(
			t,
			opts,
			bad,
			probeOf(
				ctrl,
				textOk,
				fixtures.Build(func(row *types.Verdict) {
					row.Status = types.StatusPass
				}),
			),
		).
			Run(t.Context(), textTarget)
		require.NoError(t, err)
		require.Len(t, res, wantResLenProbe)
		assert.Equal(t, types.StatusFail, res[0].Status)
		assert.Equal(t, "1 finding", res[0].Error)
		assert.Contains(t, string(res[0].Detail), "probe bad panicked: nil map")
		assert.Contains(t, string(res[0].Detail), "TestServicesProbe_PanickingProbe",
			"the stack names where the probe panicked")
		assert.Equal(t, types.StatusPass, res[1].Status)
	}
}

const (
	// wantResLenProbe is the expected res len probe.
	wantResLenProbe = 2
)
