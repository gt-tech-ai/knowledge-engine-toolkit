package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
)

// recordingInsertStore is a mock of the small consumer-side InsertOnlyStore seam (one
// method): it records the call count and returns a configured error, so a test can prove
// the tx-safe repository does NOT retry. It is a mock of the boundary, not a fake
// datastore.
type recordingInsertStore struct {
	err   error
	calls int
}

func (s *recordingInsertStore) Insert(_ context.Context, _ *int) error {
	s.calls++
	return s.err
}

// TestTxSafeInsertRepository_NoRetryInsideTx tests that the promoted tx-safe insert
// repository does NOT retry — its Insert runs inside the caller's transaction, where a
// retry would corrupt the tx.
//
// Why this test is important:
//   - The whole reason TxSafeInsertRepository exists (vs a full DecoratedRepository) is
//     that retry / circuit-breaker / timeout are UNSAFE inside a caller transaction. If a
//     future edit added a retrier to its op chain, a transient error would re-issue the
//     INSERT inside a poisoned tx — this test is the guard that the chain stays
//     retry-free.
//
// What it tests:
//   - On a retryable-classed error, the underlying store is called EXACTLY once (no
//     retry) and the error propagates.
func TestTxSafeInsertRepository_NoRetryInsideTx(t *testing.T) {
	t.Parallel()

	store := &recordingInsertStore{err: apperr.Internal("transient insert failure")}
	repo := repository.NewTxSafeInsertRepository[int]("test_outbox", store, nil, nil, nil)

	v := 7
	err := repo.Insert(context.Background(), &v)

	require.Error(t, err)
	assert.Equal(
		t,
		1,
		store.calls,
		"tx-safe insert must call the store exactly once (no retry inside a tx)",
	)
}

// TestTxSafeInsertRepository_PassThroughSuccess tests the happy path: one decorated store
// call, no error.
//
// Why this test is important:
//   - The repository is a pass-through — a success must reach the store once and return
//     nil, otherwise the outbox/audit row (the system of record) would be silently
//     dropped or double-written.
//
// What it tests:
//   - A successful Insert calls the store exactly once and returns nil.
func TestTxSafeInsertRepository_PassThroughSuccess(t *testing.T) {
	t.Parallel()

	store := &recordingInsertStore{}
	repo := repository.NewTxSafeInsertRepository[int]("test_outbox", store, nil, nil, nil)

	v := 7
	require.NoError(t, repo.Insert(context.Background(), &v))
	assert.Equal(t, 1, store.calls)
}
