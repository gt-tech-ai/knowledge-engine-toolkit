package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRepositoryBase_RunsCustomQueriesThroughTheRepositoryChain tests the embeddable
// repository base.
//
// Why this test is important:
//   - A consumer repository's custom query (anything beyond CRUD) must get the same
//     timeout, retry and breaker the decorated CRUD gets; a query outside the chain
//     hangs on a stuck database and is never retried on a transient failure.
//
// What it tests:
//   - Exec returns the query's result and is retried through the configured retrier.
//   - A query over the timeout fails with the deadline error.
//   - A zero Base runs the query undecorated.
func TestRepositoryBase_RunsCustomQueriesThroughTheRepositoryChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	retrier := mocks.NewMockRetrier(gomock.NewController(t))
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(_ context.Context, op func() error) error { return op() },
	)
	base := repository.NewBase("widgets", repository.OpDeps{
		Timeout: 20 * time.Millisecond,
		Retrier: retrier,
	})
	assert.Equal(t, "widgets", base.Name())

	got, err := base.Exec(ctx, "CountByOwner", func(context.Context) (int, error) {
		return 4, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 4, got)

	_, err = base.Exec(ctx, "Slow", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, apperr.Wrap(ctx.Err(), apperr.CodeTimeout, "slow")
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	got, err = repository.Base{}.Exec(ctx, "Plain", func(context.Context) (int, error) {
		return 1, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}
