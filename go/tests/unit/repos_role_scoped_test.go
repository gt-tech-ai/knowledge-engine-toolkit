package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// roleScopedReadStore is a mock of the small consumer-side ReadListExistStore seam
// (Get/List/Exists): it records per-method call counts and returns configured
// values/errors, so a test can prove the decorated read-only repository delegates
// each op to the store. A boundary mock, not a fake datastore.
type roleScopedReadStore struct {
	err        error
	entity     *int
	page       *types.Page[int]
	getCalls   int
	listCalls  int
	existCalls int
	exists     bool
}

func (s *roleScopedReadStore) Get(_ context.Context, _ string) (*int, error) {
	s.getCalls++
	return s.entity, s.err
}

func (s *roleScopedReadStore) List(
	_ context.Context,
	_ struct{},
	_ types.PageRequest,
) (*types.Page[int], error) {
	s.listCalls++
	return s.page, s.err
}

func (s *roleScopedReadStore) Exists(_ context.Context, _ string) (bool, error) {
	s.existCalls++
	return s.exists, s.err
}

// roleScopedCRUDStore is a mock of the small consumer-side CRUDNoListStore seam
// (Get/Create/Update/Delete/Exists): it records per-method call counts and returns
// configured values/errors. A boundary mock, not a fake datastore.
type roleScopedCRUDStore struct {
	err         error
	entity      *int
	getCalls    int
	createCalls int
	updateCalls int
	deleteCalls int
	existCalls  int
	exists      bool
}

func (s *roleScopedCRUDStore) Get(_ context.Context, _ string) (*int, error) {
	s.getCalls++
	return s.entity, s.err
}

func (s *roleScopedCRUDStore) Create(_ context.Context, entity *int) (*int, error) {
	s.createCalls++
	return entity, s.err
}

func (s *roleScopedCRUDStore) Update(
	_ context.Context,
	_ string,
	entity *int,
) (*int, error) {
	s.updateCalls++
	return entity, s.err
}

func (s *roleScopedCRUDStore) Delete(_ context.Context, _ string) error {
	s.deleteCalls++
	return s.err
}

func (s *roleScopedCRUDStore) Exists(_ context.Context, _ string) (bool, error) {
	s.existCalls++
	return s.exists, s.err
}

// TestReadListExistRepository_Delegates tests that the decorated read-only
// repository forwards each read op to its store and returns the store's result.
//
// Why this test is important:
//   - ReadListExistRepository exists so a read-only resource (e.g. a read model
//     another service writes) gets the shared decoration stack WITHOUT embedding
//     write methods it would panic on.
//     If a method stopped delegating (or wrapped the wrong store call), reads would
//     silently return wrong data.
//
// What it tests:
//   - Get/List/Exists each call the store exactly once and return its value.
func TestReadListExistRepository_Delegates(t *testing.T) {
	t.Parallel()

	want := 42
	page := &types.Page[int]{}
	store := &roleScopedReadStore{entity: &want, page: page, exists: true}
	repo := repository.NewReadListExistRepository[int, struct{}, string](
		"test_readonly", store, time.Second, nil, nil, nil, nil, nil,
	)
	ctx := context.Background()

	got, err := repo.Get(ctx, "id")
	require.NoError(t, err)
	assert.Equal(t, &want, got)

	gotPage, err := repo.List(ctx, struct{}{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Same(t, page, gotPage, "List must return the store's page unchanged")

	ok, err := repo.Exists(ctx, "id")
	require.NoError(t, err)
	assert.True(t, ok)

	assert.Equal(t, 1, store.getCalls)
	assert.Equal(t, 1, store.listCalls)
	assert.Equal(t, 1, store.existCalls)
}

// TestCRUDNoListRepository_Delegates tests that the decorated list-less repository
// forwards each op to its store and returns the store's result.
//
// Why this test is important:
//   - CRUDNoListRepository is the write-capable-but-list-less decorator (e.g. a
//     resource addressed only by id or an external id). A broken delegation would
//     corrupt or drop writes for a resource with no listable collection to
//     cross-check against.
//
// What it tests:
//   - Get/Create/Update/Delete/Exists each call the store exactly once and return
//     its value.
func TestCRUDNoListRepository_Delegates(t *testing.T) {
	t.Parallel()

	want := 42
	store := &roleScopedCRUDStore{entity: &want, exists: true}
	repo := repository.NewCRUDNoListRepository[int, string](
		"test_crud", store, time.Second, nil, nil, nil, nil, nil,
	)
	ctx := context.Background()

	got, err := repo.Get(ctx, "id")
	require.NoError(t, err)
	assert.Equal(t, &want, got)

	created, err := repo.Create(ctx, &want)
	require.NoError(t, err)
	assert.Equal(t, &want, created)

	updated, err := repo.Update(ctx, "id", &want)
	require.NoError(t, err)
	assert.Equal(t, &want, updated)

	require.NoError(t, repo.Delete(ctx, "id"))

	ok, err := repo.Exists(ctx, "id")
	require.NoError(t, err)
	assert.True(t, ok)

	assert.Equal(t, 1, store.getCalls)
	assert.Equal(t, 1, store.createCalls)
	assert.Equal(t, 1, store.updateCalls)
	assert.Equal(t, 1, store.deleteCalls)
	assert.Equal(t, 1, store.existCalls)
}

// TestCRUDNoListRepository_CreateIsRetryFreeButReadsRetry tests that Create runs
// through the retry-FREE chain while read/update/delete run through the retrying
// chain.
//
// Why this test is important:
//   - This two-chain split is the entire reason CRUDNoListRepository builds a
//
// separate createChain: a create is not idempotent, so blindly
//
//	retrying one that actually landed post-commit would insert a duplicate. If a
//	future edit routed Create through the retrying chain, a transient error would
//	double-insert — this test is the guard against that regression.
//
// What it tests:
//   - On an always-failing store, a read op (Get) is retried by the resilient chain
//     (store called 3×), while Create is called exactly once (no retry).
func TestCRUDNoListRepository_CreateIsRetryFreeButReadsRetry(t *testing.T) {
	t.Parallel()

	retrier, attempts := fixtures.StubRetrier(0, true) // alwaysFail → up to 3 attempts
	store := &roleScopedCRUDStore{err: apperr.Internal("transient failure")}
	repo := repository.NewCRUDNoListRepository[int, string](
		"test_crud", store, 0, nil, nil, nil, retrier, nil,
	)
	ctx := context.Background()

	_, err := repo.Get(ctx, "id")
	require.Error(t, err)
	assert.Equal(t, 3, store.getCalls, "reads must retry through the resilient chain")

	v := 7
	_, err = repo.Create(ctx, &v)
	require.Error(t, err)
	assert.Equal(
		t,
		1,
		store.createCalls,
		"create must NOT retry (non-idempotent) even on a retryable error",
	)

	assert.Positive(t, attempts(), "the retrier must have engaged for the read path")
}
