package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/deadletter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestDeadLetterQueue_SwallowsBackendFailureAndRedrives tests the swallow-and-report
// contract: a backend failure never propagates (the consumer survives) but Send returns
// false so the caller leaves the source message for redrive (no silent data loss).
//
// Why this test is important:
//   - A DLQ outage that crashed the consumer, or that silently "succeeded" and let the
//     caller delete the source message, would both lose data. The false return is the
//     signal that lets the caller redrive instead.
//
// What it tests:
//   - A failing backend yields Send == false (logged, not panicked); the in-memory stub
//     backend yields Send == true and records the letter.
func TestDeadLetterQueue_SwallowsBackendFailureAndRedrives(t *testing.T) {
	t.Parallel()

	letter := interfaces.DeadLetter{ID: "m1", Payload: []byte("x"), Reason: "poison"}

	// Backend failure → false (swallowed + logged), consumer survives. The mock
	// backend's Send fails exactly once, modelling a DLQ outage.
	ctrl := gomock.NewController(t)
	failingBackend := mocks.NewMockDeadLetterBackend(ctrl)
	failingBackend.EXPECT().
		Send(gomock.Any(), gomock.Any()).
		Return(apperr.Sentinel("dlq unavailable"))
	failing := deadletter.New(failingBackend, fixtures.NewSpyLogger())
	require.False(t, failing.Send(context.Background(), letter),
		"a backend failure must return false so the caller redrives the source message")

	// Success → true and the letter is durably recorded.
	stub := &deadletter.StubBackend{}
	q := deadletter.New(stub, fixtures.NewSpyLogger())
	require.True(t, q.Send(context.Background(), letter))
	require.Len(t, stub.Letters, 1)
	require.Equal(t, "m1", stub.Letters[0].ID)
}
