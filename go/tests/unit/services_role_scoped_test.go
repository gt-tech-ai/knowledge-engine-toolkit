package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
)

// roleScopedCRUDServiceRepo is a mock of the small core CRUDNoListRepository
// seam a list-less service delegates to (Get/Create/Update/Delete — services expose
// no Exists): it records per-method call counts and returns configured
// values/errors. A boundary mock, not a fake repository.
type roleScopedCRUDServiceRepo struct {
	entity      *int
	err         error
	getCalls    int
	createCalls int
	updateCalls int
	deleteCalls int
}

func (r *roleScopedCRUDServiceRepo) Get(_ context.Context, _ string) (*int, error) {
	r.getCalls++
	return r.entity, r.err
}

func (r *roleScopedCRUDServiceRepo) Create(_ context.Context, entity *int) (*int, error) {
	r.createCalls++
	return entity, r.err
}

func (r *roleScopedCRUDServiceRepo) Update(
	_ context.Context,
	_ string,
	entity *int,
) (*int, error) {
	r.updateCalls++
	return entity, r.err
}

func (r *roleScopedCRUDServiceRepo) Delete(_ context.Context, _ string) error {
	r.deleteCalls++
	return r.err
}

// TestCRUDNoListService_Delegates tests that the decorated list-less service
// forwards each op to its repository and returns the repository's result.
//
// Why this test is important:
//   - CRUDNoListService is the service-tier decorator for a resource whose
//     collection is not listable (e.g. one addressed only by id). It carries the
//     recovery/observability/timeout stack without embedding a List it would panic
//     on; a broken delegation would silently serve wrong data or drop a write.
//
// What it tests:
//   - Get/Create/Update/Delete each call the repository exactly once and return its
//     value.
func TestCRUDNoListService_Delegates(t *testing.T) {
	t.Parallel()

	want := 42
	repo := &roleScopedCRUDServiceRepo{entity: &want}
	svc := service.NewCRUDNoListService[int, string](
		"test_users", repo, time.Second, nil, nil, nil, nil,
	)
	ctx := context.Background()

	got, err := svc.Get(ctx, "id")
	require.NoError(t, err)
	assert.Equal(t, &want, got)

	created, err := svc.Create(ctx, &want)
	require.NoError(t, err)
	assert.Equal(t, &want, created)

	updated, err := svc.Update(ctx, "id", &want)
	require.NoError(t, err)
	assert.Equal(t, &want, updated)

	require.NoError(t, svc.Delete(ctx, "id"))

	assert.Equal(t, 1, repo.getCalls)
	assert.Equal(t, 1, repo.createCalls)
	assert.Equal(t, 1, repo.updateCalls)
	assert.Equal(t, 1, repo.deleteCalls)
}
