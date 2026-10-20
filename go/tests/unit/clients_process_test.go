package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/process"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// readyAfter returns a readiness probe that passes on its nth run, counting runs.
func readyAfter(
	t *testing.T,
	n int32,
) (*mocks.MockProbe[interfaces.ProcessHandle], *atomic.Int32) {
	t.Helper()
	var runs atomic.Int32
	p := mocks.NewMockProbe[interfaces.ProcessHandle](gomock.NewController(t))
	p.EXPECT().Name().Return("ready").AnyTimes()
	p.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, interfaces.ProcessHandle) types.Verdict {
			if runs.Add(1) >= n {
				return types.Verdict{Status: types.StatusPass}
			}
			return types.Verdict{Status: types.StatusFail}
		}).AnyTimes()
	return p, &runs
}

// sleeper is a process that runs until stopped.
func sleeper() types.ProcessSpec {
	return types.ProcessSpec{Name: "sleeper", Args: []string{"/bin/sh", "-c", "sleep 30"}}
}

// TestClientsProcess_StartReadyStop tests the exec supervisor's lifecycle.
//
// Why this test is important:
//   - A dev stack starts servers and port-forwards, waits until each answers, and must
//     stop them all on exit; a supervisor that returns before ready races the next
//     step, and one that leaks a process holds a port until reboot.
//
// What it tests:
//   - Start returns once readiness passes on its third check; Stop ends the process
//     quickly and Wait then returns; a process never ready within ctx is stopped and
//     the error is TIMEOUT; one that exits first is UPSTREAM; StopAll stops what is
//     left; an empty command is INVALID_INPUT.
func TestClientsProcess_StartReadyStop(t *testing.T) {
	t.Parallel()
	sup, err := process.NewFromConfig(process.Config{Kind: process.KindExec})
	require.NoError(t, err)
	ready, runs := readyAfter(t, 3)

	h, err := sup.Start(t.Context(), sleeper(), ready)
	require.NoError(t, err)
	assert.Equal(t, int32(3), runs.Load())
	stopped := time.Now()
	require.NoError(t, h.Stop(t.Context()))
	assert.Less(t, time.Since(stopped), 5*time.Second)
	require.Error(t, h.Wait())

	never, _ := readyAfter(t, 1000)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err = sup.Start(ctx, sleeper(), never)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))

	_, err = sup.Start(
		t.Context(),
		types.ProcessSpec{
			Name: "quitter",
			Args: []string{"/bin/sh", "-c", "exit 3"},
		},
		never,
	)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))

	left, err := sup.Start(t.Context(), sleeper(), nil)
	require.NoError(t, err)
	require.NoError(t, sup.StopAll(t.Context()))
	require.Error(t, left.Wait())

	_, err = sup.Start(t.Context(), types.ProcessSpec{Name: "empty"}, nil)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestClientsProcess_DetachAndStub tests detaching, the stub and the factory.
//
// Why this test is important:
//   - A detached helper (a model server) must outlive the command that started it;
//     the stub is what a composition root boots on without starting anything.
//
// What it tests:
//   - Detach starts a process and returns; the default kind starts nothing and its
//     handles return at once; an unknown kind is INVALID_INPUT.
func TestClientsProcess_DetachAndStub(t *testing.T) {
	t.Parallel()
	sup, err := process.NewFromConfig(process.Config{Kind: process.KindExec})
	require.NoError(t, err)
	require.NoError(
		t,
		sup.Detach(
			types.ProcessSpec{Name: "d", Args: []string{"/bin/sh", "-c", "exit 0"}},
		),
	)

	stub, err := process.NewFromConfig(process.Config{})
	require.NoError(t, err)
	h, err := stub.Start(t.Context(), sleeper(), nil)
	require.NoError(t, err)
	require.NoError(t, h.Wait())

	_, err = process.NewFromConfig(process.Config{Kind: "systemd"})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
