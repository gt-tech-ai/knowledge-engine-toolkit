package unit_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	jobdec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/decorators"
	msgdec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/deadletter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/dedup"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/leader"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestWrapHandler_DedupSkipsDuplicate tests that the EventHandler stack skips a message
// whose key was already processed, so a redelivery is handled exactly once.
//
// Why this test is important:
//   - Under at-least-once delivery the same message can arrive twice; the dedup layer is
//     the guard, and re-running the handler would double-process (e.g. duplicate side
//     effects).
//
// What it tests:
//   - Two handler invocations for the same message id run the inner handler exactly once.
func TestWrapHandler_DedupSkipsDuplicate(t *testing.T) {
	t.Parallel()

	var calls int32
	handler := msgdec.WrapHandler(
		func(context.Context, *interfaces.Message) error {
			atomic.AddInt32(&calls, 1)
			return nil
		},
		msgdec.EventStackDeps{Dedup: dedup.NewMemory(0), Name: "test"},
	)

	msg := &interfaces.Message{ID: "m1", Topic: "t"}
	require.NoError(t, handler(context.Background(), msg), "first delivery processed")
	require.NoError(t, handler(context.Background(), msg), "duplicate delivery skipped")
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "handler must run exactly once")
}

// TestWrapHandler_TerminalFailureDeadLettered tests that a message that still fails after
// the resilience stack is routed to the dead-letter queue and acked.
//
// Why this test is important:
//   - A poison message that neither dead-letters nor acks would loop forever; routing it
//     to the DLQ and acking is how the consumer makes progress without losing the
//     message.
//
// What it tests:
//   - An always-failing handler (no retrier → one attempt) yields a nil return (acked)
//     and the dead-letter backend records the message.
func TestWrapHandler_TerminalFailureDeadLettered(t *testing.T) {
	t.Parallel()

	stub := &deadletter.StubBackend{}
	handler := msgdec.WrapHandler(
		func(context.Context, *interfaces.Message) error { return apperr.Sentinel("boom") },
		msgdec.EventStackDeps{
			DeadLetter: deadletter.New(stub, fixtures.NewSpyLogger()),
			Name:       "test",
		},
	)

	msg := &interfaces.Message{ID: "m1", Topic: "t", Payload: []byte("x")}
	require.NoError(
		t,
		handler(context.Background(), msg),
		"a dead-lettered message is acked",
	)
	require.Len(t, stub.Letters, 1)
	require.Equal(t, "m1", stub.Letters[0].ID)
}

// TestWrapJob_LeaderGating tests that the Job stack runs the job only on the leader, so a
// job scheduled across N replicas fires once.
//
// Why this test is important:
//   - Running a scheduled job on every replica would duplicate its effect (e.g. N reaper
//     sweeps); leader election is the guard that makes it fire once.
//
// What it tests:
//   - A non-leader instance skips the job (never runs it); the leader runs it exactly
//     once.
func TestWrapJob_LeaderGating(t *testing.T) {
	t.Parallel()

	var followerCalls, leaderCalls int32

	// notLeader is a generated LeaderElector mock that never holds leadership, so
	// the follower path must skip the job.
	notLeader := mocks.NewMockLeaderElector(gomock.NewController(t))
	notLeader.EXPECT().IsLeader(gomock.Any()).Return(false, nil).AnyTimes()

	follower := jobdec.WrapJob(
		func(context.Context) error { atomic.AddInt32(&followerCalls, 1); return nil },
		jobdec.JobStackDeps{Leader: notLeader, Name: "test"},
	)
	require.NoError(t, follower(context.Background()))
	require.Zero(t, atomic.LoadInt32(&followerCalls), "a non-leader must not run the job")

	elected := jobdec.WrapJob(
		func(context.Context) error { atomic.AddInt32(&leaderCalls, 1); return nil },
		jobdec.JobStackDeps{Leader: leader.AlwaysLeader{}, Name: "test"},
	)
	require.NoError(t, elected(context.Background()))
	require.Equal(
		t,
		int32(1),
		atomic.LoadInt32(&leaderCalls),
		"the leader runs the job once",
	)
}
