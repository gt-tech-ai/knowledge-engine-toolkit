package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestServiceTier_Order tests the service tier's decorator order.
//
// Why this test is important:
//   - The order is the tier's documented contract (ARCHITECTURE.md#decorator-order):
//     authorization must run before the timeout starts and inside the observability
//     layers, so a denial is traced, metered and logged.
//
// What it tests:
//   - The tier is named "service" and orders recovery → tracing → metrics → logging →
//     authorization → timeout.
func TestServiceTier_Order(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "service", Order: []decorate.Layer{
		decorate.LayerRecovery, decorate.LayerTracing, decorate.LayerMetrics,
		decorate.LayerLogging, decorate.LayerAuth, decorate.LayerTimeout,
	}}, service.Tier())
}

// counterService is the counting service's contract.
type counterService interface {
	// Count returns the count.
	Count(ctx context.Context) (int, error)

	// Slow waits for its deadline.
	Slow(ctx context.Context) (int, error)

	// Boom panics.
	Boom(ctx context.Context) (int, error)
}

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
		"count",
		func(context.Context) (int, error) { return s.count, nil },
	)
}

// TestServiceBase_DecoratesAnEmbeddingServicesOperations tests the embeddable base.
//
// Why this test is important:
//   - A consumer's hand-written service gets the tier's decoration by embedding Base and
//     calling Exec; skipped decoration lets an operation hang a request or crash the
//     process on a panic.
//
// What it tests:
//   - Exec, promoted through embedding, returns the operation's result; Name is the
//     service's.
//   - An operation over the timeout ends with the deadline; a panic is an INTERNAL
//     error; a zero Base runs the operation undecorated.
func TestServiceBase_DecoratesAnEmbeddingServicesOperations(t *testing.T) {
	t.Parallel()
	base := fixtures.Must(
		service.NewBase[counterService](
			decorate.Fixed(decorate.Deps{Timeout: 20 * time.Millisecond}),
			"counter",
		),
	)
	assert.Equal(t, "counter", base.Name())

	got, err := countingService{Base: base, count: 3}.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, got)

	_, err = base.Exec(t.Context(), "slow", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = base.Exec(t.Context(), "boom", func(context.Context) (int, error) {
		panic("boom")
	})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	got, err = countingService{count: 5}.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 5, got)
}

// TestServiceBaseCRUD_DelegatesValidatesAndAuthorizes tests the decorated CRUD
// services.
//
// Why this test is important:
//   - The CRUD service is where an entity is validated and an action authorized before
//     it reaches the repository; a skipped check writes an invalid or unauthorized
//     entity.
//
// What it tests:
//   - Every operation reaches the repository and returns its result.
//   - An invalid entity is rejected before Create or Update reaches the repository.
//   - A denied "<service>.<op>" action is returned before the repository is called.
//   - CRUDNoList serves Get, Create, Update and Delete the same way.
func TestServiceBaseCRUD_DelegatesValidatesAndAuthorizes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository[te, tp, string](ctrl)
	invalid := apperr.New(apperr.CodeInvalidInput, "name required")
	validate := func(e *te) error {
		if e.Name == "" {
			return invalid
		}
		return nil
	}
	denied := apperr.New(apperr.CodeForbidden, "denied")
	var actions []string
	svc := fixtures.Must(service.NewBase[interfaces.Service[te, tp, string]](
		decorate.Fixed(decorate.Deps{
			Auth: func(_ context.Context, action string) error {
				actions = append(actions, action)
				if action == "widgets.delete" {
					return denied
				}
				return nil
			},
		}), "widgets")).
		CRUD(repo, validate)
	w := &te{ID: "w1", Name: "one"}
	repo.EXPECT().Get(gomock.Any(), "w1").Return(w, nil)
	repo.EXPECT().
		List(gomock.Any(), tp{}, gomock.Any()).
		Return(&types.Page[te]{Total: 1}, nil)
	repo.EXPECT().Create(gomock.Any(), w).Return(w, nil)
	repo.EXPECT().Update(gomock.Any(), "w1", w).Return(w, nil)

	got, err := svc.Get(t.Context(), "w1")
	require.NoError(t, err)
	assert.Same(t, w, got)
	page, err := svc.List(t.Context(), tp{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	_, err = svc.Create(t.Context(), w)
	require.NoError(t, err)
	_, err = svc.Update(t.Context(), "w1", w)
	require.NoError(t, err)
	_, err = svc.Create(t.Context(), &te{ID: "w2"})
	require.ErrorIs(t, err, invalid)
	require.ErrorIs(t, svc.Delete(t.Context(), "w1"), denied)
	assert.Equal(t, []string{
		"widgets.get", "widgets.list", "widgets.create", "widgets.update",
		"widgets.create", "widgets.delete",
	}, actions)

	keyed := mocks.NewMockCRUDNoListRepository[te, string](ctrl)
	keyed.EXPECT().Delete(gomock.Any(), "w1").Return(nil)
	noList := fixtures.Must(service.NewBase[interfaces.CRUDNoListRepository[te, string]](
		fixtures.Decorations(), "keyed",
	)).
		CRUDNoList(keyed, validate)
	require.NoError(t, noList.Delete(t.Context(), "w1"))
	_, err = noList.Update(t.Context(), "w1", &te{})
	require.ErrorIs(t, err, invalid)
}
