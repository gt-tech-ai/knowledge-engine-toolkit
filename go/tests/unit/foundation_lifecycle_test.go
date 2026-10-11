package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// newRecordingLifecycle builds a MockLifecycle that appends its start/stop event to
// the shared log when Start/Stop are called, returning the injected startErr/stopErr,
// so a test can assert the Manager's start and stop ORDER (the observable behaviour)
// and exercise rollback / continue-past-failure. Start and Stop are each registered
// exactly once with AnyTimes because a component may be started or stopped zero or one
// time depending on the scenario (a component after a failed Start is never reached);
// the log-equality assertion — not a strict call count — is what proves the order and
// which components were skipped, exactly as the earlier spy did.
func newRecordingLifecycle(
	ctrl *gomock.Controller,
	name string,
	log *[]string,
	startErr, stopErr error,
) *mocks.MockLifecycle {
	c := mocks.NewMockLifecycle(ctrl)
	c.EXPECT().Start(gomock.Any()).DoAndReturn(func(context.Context) error {
		*log = append(*log, "start:"+name)
		return startErr
	}).AnyTimes()
	c.EXPECT().Stop(gomock.Any()).DoAndReturn(func(context.Context) error {
		*log = append(*log, "stop:"+name)
		return stopErr
	}).AnyTimes()
	return c
}

// TestLifecycleManager_StartsInOrderStopsInReverse tests that the Manager starts
// registered clients in registration order and stops them in the reverse order.
//
// Why this test is important:
//   - Dependencies must come up before dependents and shut down after them; a
//     wrong order tears a dependency down while a dependent is still using it.
//
// What it tests:
//   - Start runs a→b→c; Stop runs c→b→a.
func TestLifecycleManager_StartsInOrderStopsInReverse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	var log []string
	m := lifecycle.New()
	m.Register("a", newRecordingLifecycle(ctrl, "a", &log, nil, nil))
	m.Register("b", newRecordingLifecycle(ctrl, "b", &log, nil, nil))
	m.Register("c", newRecordingLifecycle(ctrl, "c", &log, nil, nil))

	require.NoError(t, m.Start(context.Background()))
	require.NoError(t, m.Stop(context.Background()))

	assert.Equal(t, []string{
		"start:a", "start:b", "start:c",
		"stop:c", "stop:b", "stop:a",
	}, log)
}

// TestLifecycleManager_RollsBackOnStartFailure tests that when a client fails to
// start, the Manager stops the clients already started (in reverse) and returns the
// error, leaving later clients unstarted.
//
// Why this test is important:
//   - A partial startup that isn't rolled back leaks half-open resources (open
//     pools, dangling connections) and hides the real failure.
//
// What it tests:
//   - a starts, b fails → a is stopped, c never starts, and Start returns an error.
func TestLifecycleManager_RollsBackOnStartFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	var log []string
	m := lifecycle.New()
	m.Register("a", newRecordingLifecycle(ctrl, "a", &log, nil, nil))
	m.Register("b", newRecordingLifecycle(ctrl, "b", &log, apperr.Sentinel("boom"), nil))
	m.Register("c", newRecordingLifecycle(ctrl, "c", &log, nil, nil))

	err := m.Start(context.Background())

	require.Error(t, err)
	assert.Equal(t, []string{"start:a", "start:b", "stop:a"}, log)
}

// TestLifecycleManager_StopContinuesPastFailure tests that Stop stops every client
// even when one fails, in reverse order, and returns the first error encountered.
//
// Why this test is important:
//   - Shutdown must always complete for every client; bailing on the first Stop
//     error would leak the remaining clients' resources.
//
// What it tests:
//   - With b's Stop failing, c→b→a all stop and Stop returns a non-nil error.
func TestLifecycleManager_StopContinuesPastFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	var log []string
	m := lifecycle.New()
	m.Register("a", newRecordingLifecycle(ctrl, "a", &log, nil, nil))
	m.Register("b", newRecordingLifecycle(ctrl, "b", &log, nil, apperr.Sentinel("boom")))
	m.Register("c", newRecordingLifecycle(ctrl, "c", &log, nil, nil))

	require.NoError(t, m.Start(context.Background()))
	log = log[:0] // observe only the stop order

	err := m.Stop(context.Background())

	require.Error(t, err)
	assert.Equal(t, []string{"stop:c", "stop:b", "stop:a"}, log)
}
