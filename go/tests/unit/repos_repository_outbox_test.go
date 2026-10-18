package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRepositoryOutbox_ClaimsOnceAndRetriesTheRest tests the decorated outbox
// repository a relay programs to.
//
// Why this test is important:
//   - Claim leases rows and counts an attempt, so a retried Claim would lease a second
//     batch and burn attempts the relay never made; the finalizers, Stats and the sweep
//     are idempotent and must get the retry, or one transient failure leaves a delivered
//     row pending until its lease expires.
//   - Every operation must stay bounded by the repository's timeout.
//
// What it tests:
//   - With a retrier configured, Claim runs without it (the retrier is never asked),
//     while MarkSent, Retry, Park, Stats and DeleteSentBefore each run through it once.
//   - Every operation reaches the store with the repository's deadline and returns the
//     store's result and coded error unchanged.
func TestRepositoryOutbox_ClaimsOnceAndRetriesTheRest(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	repo := fixtures.Must(repository.NewBase[interfaces.OutboxRepository](
		decorate.Fixed(decorate.Deps{
			Timeout: time.Minute,
			Retrier: passThroughRetrier(t, 5),
		}), "outbox")).
		Outbox(store)

	bounded := gomock.Cond(func(ctx context.Context) bool {
		_, ok := ctx.Deadline()
		return ok
	})
	id, next, cutoff := uuid.New(), time.Unix(100, 0), time.Unix(50, 0)
	claimed := []types.OutboxRecord{{ID: id, Lane: "facts"}}
	conflict := apperr.New(apperr.CodeConflict, "not pending")
	store.EXPECT().Claim(bounded, "facts", 10).Return(claimed, nil)
	store.EXPECT().MarkSent(bounded, id).Return(conflict)
	store.EXPECT().Retry(bounded, id, next, "boom").Return(nil)
	store.EXPECT().Park(bounded, id, "boom").Return(nil)
	store.EXPECT().Stats(bounded, "facts").Return(types.OutboxStats{Pending: 3}, nil)
	store.EXPECT().DeleteSentBefore(bounded, cutoff).Return(7, nil)

	got, err := repo.Claim(t.Context(), "facts", 10)
	require.NoError(t, err)
	assert.Equal(t, claimed, got)
	assert.Equal(t, apperr.CodeConflict, apperr.Code(repo.MarkSent(t.Context(), id)))
	require.NoError(t, repo.Retry(t.Context(), id, next, "boom"))
	require.NoError(t, repo.Park(t.Context(), id, "boom"))
	stats, err := repo.Stats(t.Context(), "facts")
	require.NoError(t, err)
	assert.Equal(t, types.OutboxStats{Pending: 3}, stats)
	n, err := repo.DeleteSentBefore(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, 7, n)
}
