package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestNewOpChain_AppliesTheBundledDeps tests the OpDeps form of the custom-op chain.
//
// Why this test is important:
//   - Repositories build their chain from one OpDeps value; a field the bundle dropped
//     would silently strip a retry or a timeout from every operation.
//
// What it tests:
//   - The bundled Retrier wraps the operation, and the bundled Timeout bounds it
//     (an operation outliving it ends CodeTimeout).
//   - An empty OpDeps runs the operation undecorated.
func TestNewOpChain_AppliesTheBundledDeps(t *testing.T) {
	t.Parallel()
	retrier := mocks.NewMockRetrier(gomock.NewController(t))
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error { return op() })
	chain := repository.NewOpChain("budget", repository.OpDeps{
		Retrier: retrier,
		Timeout: 10 * time.Millisecond,
	})
	_, err := decorate.Exec(
		t.Context(),
		chain,
		"slow",
		func(c context.Context) (int, error) {
			<-c.Done()
			return 0, apperr.Wrap(
				c.Err(),
				apperr.ContextCode(c.Err(), apperr.CodeCanceled),
				"slow",
			)
		},
	)
	require.Equal(t, apperr.CodeTimeout, apperr.Code(err))

	got, err := decorate.Exec(
		t.Context(),
		repository.NewOpChain("budget", repository.OpDeps{}),
		"fast",
		func(context.Context) (int, error) { return 7, nil },
	)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}
