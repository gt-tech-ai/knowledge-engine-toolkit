package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	outboxrepo "github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/outbox"
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
	repo := fixtures.Must(outboxrepo.New(
		decorate.Fixed(fixtures.BuildRef(func(row *decorate.Deps) {
			row.Timeout = time.Minute
			row.Retrier = passThroughRetrier(t, 5)
		})), "outbox", store))

	bounded := gomock.Cond(func(ctx context.Context) bool {
		_, ok := ctx.Deadline()

		return ok
	})
	identifier, next, cutoff := uuid.New(), time.Unix(
		unixInputRest,
		0,
	), time.Unix(
		unixInputTheRest,
		0,
	)
	claimed := []types.OutboxRecord{
		fixtures.Build(func(row *types.OutboxRecord) {
			row.ID = identifier
			row.Lane = textFacts
		}),
	}
	conflict := apperr.New(apperr.CodeConflict, "not pending")

	store.EXPECT().Claim(bounded, textFacts, 0, 10).Return(claimed, nil)
	store.EXPECT().MarkSent(bounded, identifier).Return(conflict)
	store.EXPECT().Retry(bounded, identifier, next, textBoom).Return(nil)

	testRepositoryOutboxClaimsOnceAndRetriesTheRest(
		t,
		store,
		repo,
		bounded,
		identifier,
		next,
		cutoff,
		claimed,
	)
}

// testRepositoryOutboxClaimsOnceAndRetriesTheRest continues
// TestRepositoryOutbox_ClaimsOnceAndRetriesTheRest.
func testRepositoryOutboxClaimsOnceAndRetriesTheRest(
	t *testing.T,
	store *mocks.MockOutboxStore,
	repo interfaces.OutboxRepository,
	bounded gomock.Matcher,
	identifier uuid.UUID,
	next, cutoff time.Time,
	claimed []types.OutboxRecord,
) {
	t.Helper()

	store.EXPECT().Park(bounded, identifier, textBoom).Return(nil)
	store.EXPECT().
		Stats(bounded, textFacts).
		Return(fixtures.Build(func(row *types.OutboxStats) {
			row.Pending = 3
		}), nil)
	store.EXPECT().DeleteSentBefore(bounded, cutoff).Return(returnInput, nil)

	got, err := repo.Claim(t.Context(), textFacts, 0, claimInput)
	require.NoError(t, err)
	assert.Equal(t, claimed, got)
	assert.Equal(
		t,
		apperr.CodeConflict,
		apperr.Code(repo.MarkSent(t.Context(), identifier)),
	)
	require.NoError(t, repo.Retry(t.Context(), identifier, next, textBoom))
	require.NoError(t, repo.Park(t.Context(), identifier, textBoom))

	stats, err := repo.Stats(t.Context(), textFacts)
	require.NoError(t, err)
	assert.Equal(
		t,
		fixtures.Build(func(row *types.OutboxStats) {
			row.Pending = 3
		}),
		stats,
	)

	n, err := repo.DeleteSentBefore(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, wantN, n)
}

const (
	// unixInputRest is the sec passed to Unix in
	// TestRepositoryOutbox_ClaimsOnceAndRetriesTheRest.
	unixInputRest = 100

	// unixInputTheRest is the sec passed to Unix in
	// TestRepositoryOutbox_ClaimsOnceAndRetriesTheRest.
	unixInputTheRest = 50
)

const (
	// textFacts is the literal "facts".
	textFacts = "facts"
)
