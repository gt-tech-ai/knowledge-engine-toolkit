package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
)

// countingService is a consumer service that embeds service.Base and runs its one
// custom operation through the promoted Exec.
type countingService struct {
	service.Base

	// count is what the operation returns.
	count int
}

// Count is the decorated custom operation.
func (s countingService) Count(ctx context.Context) (int, error) {
	return s.Exec(
		ctx,
		"Count",
		func(context.Context) (int, error) { return s.count, nil },
	)
}

// TestServiceBase_DecoratesAnEmbeddingServicesOperations tests the embeddable service
// base.
//
// Why this test is important:
//   - A consumer's hand-written service gets the tier's decoration chain by embedding
//     Base and calling Exec; if the chain were skipped, an operation could hang a request
//     forever or crash the process on a panic.
//
// What it tests:
//   - Exec, promoted through embedding, returns the operation's result.
//   - An operation over the timeout fails with the deadline error.
//   - A panicking operation fails with an INTERNAL error instead of unwinding.
//   - A zero Base runs the operation undecorated.
func TestServiceBase_DecoratesAnEmbeddingServicesOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := service.NewBase("counter", decorate.Observability{}, 20*time.Millisecond)
	assert.Equal(t, "counter", base.Name())

	got, err := countingService{Base: base, count: 3}.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, got)

	_, err = base.Exec(ctx, "Slow", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = base.Exec(ctx, "Boom", func(context.Context) (int, error) { panic("boom") })
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	got, err = countingService{count: 5}.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, got)
}
