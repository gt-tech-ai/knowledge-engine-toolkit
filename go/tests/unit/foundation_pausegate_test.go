package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/pausegate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestPauseGate_ClosesForThePauseOnAnOutage tests the gate's state machine.
//
// Why this test is important:
//   - A consumer that kept receiving during a downstream outage would burn each
//     message's delivery count into the dead-letter queue; one that never reopened
//     would stall.
//
// What it tests:
//   - Nil and non-outage errors leave the gate open; a CodeUnavailable error
//     closes it (Ready is CodeUnavailable, the gauge reads 1) until the pause
//     elapses, then it reopens (the gauge reads 0).
func TestPauseGate_ClosesForThePauseOnAnOutage(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000, 0)
	gauge := mocks.NewMockGauge(gomock.NewController(t))
	gate, err := pausegate.New(pausegate.Config{Pause: 30 * time.Second}, gauge,
		func() time.Time { return now })
	require.NoError(t, err)

	gauge.EXPECT().Set(float64(0)).AnyTimes()
	gate.Observe(nil)
	gate.Observe(apperr.New(apperr.CodeInvalidInput, "bad message"))
	require.NoError(t, gate.Ready(t.Context()))

	gauge.EXPECT().Set(float64(1))
	gate.Observe(apperr.New(apperr.CodeUnavailable, "store down"))
	require.Equal(t, apperr.CodeUnavailable, apperr.Code(gate.Ready(t.Context())))

	now = now.Add(29 * time.Second)
	require.Error(t, gate.Ready(t.Context()))
	now = now.Add(time.Second)
	require.NoError(t, gate.Ready(t.Context()))
}

// TestPauseGate_RejectsANonPositivePause tests config validation.
//
// Why this test is important:
//   - A zero pause would never close the gate, silently disabling the protection.
//
// What it tests:
//   - Pause <= 0 is CodeInvalidInput.
func TestPauseGate_RejectsANonPositivePause(t *testing.T) {
	t.Parallel()
	_, err := pausegate.New(pausegate.Config{}, nil, time.Now)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
