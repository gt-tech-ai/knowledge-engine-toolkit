package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// widgetStore is the test entity's store contract: CRUD, the append and upsert writes,
// the retention purge, and one product query.
type widgetStore interface {
	interfaces.Store[te, tp, string]
	interfaces.Inserter[te]
	interfaces.Upserter[te]
	interfaces.Purger

	// CountByOwner counts an owner's widgets.
	CountByOwner(ctx context.Context, owner string) (int, error)
}

// widgetReads is a read-only store contract: Get, List and Exists.
type widgetReads interface {
	interfaces.ReadListExistStore[te, tp, string]
}

// widgetService is the test entity's service contract.
type widgetService interface {
	interfaces.Service[te, tp, string]
}

// widgetNext is a next tier with every role a widget component can serve.
type widgetNext struct {
	*mocks.MockStore[te, tp, string]
	*mocks.MockInserter[te]
	*mocks.MockUpserter[te]
	*mocks.MockPurger
}

// newWidgetNext returns a strict widgetNext over ctrl.
func newWidgetNext(ctrl *gomock.Controller) widgetNext {
	return widgetNext{
		MockStore:    mocks.NewMockStore[te, tp, string](ctrl),
		MockInserter: mocks.NewMockInserter[te](ctrl),
		MockUpserter: mocks.NewMockUpserter[te](ctrl),
		MockPurger:   mocks.NewMockPurger(ctrl),
	}
}

// expectEveryWidgetRole scripts one successful call of each of the nine roles on n.
func expectEveryWidgetRole(currentWidgetNext widgetNext) {
	testEntity := &te{ID: textW1, Name: ""}
	currentWidgetNext.MockStore.EXPECT().Get(gomock.Any(), textW1).Return(testEntity, nil)
	currentWidgetNext.MockStore.EXPECT().List(gomock.Any(), tp{}, gomock.Any()).
		Return(fixtures.BuildRef(func(row *types.Page[te]) {
			row.Total = 1
		}), nil)
	currentWidgetNext.MockStore.EXPECT().
		Create(gomock.Any(), testEntity).
		Return(testEntity, nil)
	currentWidgetNext.MockStore.EXPECT().
		Update(gomock.Any(), textW1, testEntity).
		Return(testEntity, nil)
	currentWidgetNext.MockStore.EXPECT().Delete(gomock.Any(), textW1).Return(nil)
	currentWidgetNext.MockStore.EXPECT().Exists(gomock.Any(), textW1).Return(true, nil)
	currentWidgetNext.MockInserter.EXPECT().Insert(gomock.Any(), testEntity).Return(nil)
	currentWidgetNext.MockUpserter.EXPECT().Upsert(gomock.Any(), testEntity).Return(nil)
	currentWidgetNext.MockPurger.EXPECT().
		PurgeBefore(gomock.Any(), gomock.Any()).
		Return(returnInputRole, nil)
}

// callEveryWidgetRole calls each of r's nine roles once on ctx, requiring each to
// succeed and return what the next tier returned.
func callEveryWidgetRole(ctx context.Context, t *testing.T, value interface {
	interfaces.Store[te, tp, string]
	interfaces.Inserter[te]
	interfaces.Upserter[te]
	interfaces.Purger
},
) {
	t.Helper()

	testEntity := &te{ID: textW1, Name: ""}
	got, err := value.Get(ctx, textW1)
	require.NoError(t, err)
	assert.Equal(t, textW1, got.ID)

	page, err := value.List(ctx, tp{}, stdx.Zero[types.PageRequest]())
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)

	_, err = value.Create(ctx, testEntity)
	require.NoError(t, err)

	_, err = value.Update(ctx, textW1, testEntity)
	require.NoError(t, err)
	require.NoError(t, value.Delete(ctx, textW1))

	ok, err := value.Exists(ctx, textW1)
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, value.Insert(ctx, testEntity))
	require.NoError(t, value.Upsert(ctx, testEntity))

	n, err := value.PurgeBefore(ctx, time.Unix(0, 0))
	require.NoError(t, err)
	assert.Equal(t, wantNRole, n)
}

// widgetFuncs returns the statements of every widget role, each recording the client it
// ran on in ran under its role.
func widgetFuncs(ran map[string]string) store.Funcs[*storeClient, te, tp, string] {
	return store.Funcs[*storeClient, te, tp, string]{
		Get: func(_ context.Context, c *storeClient, id string) (*te, error) {
			ran[textGetTitle] = c.name

			return &te{ID: id, Name: ""}, nil
		},
		List: func(
			_ context.Context,
			c *storeClient,
			_ tp,
			_ types.PageRequest,
		) (*types.Page[te], error) {
			ran["List"] = c.name

			return fixtures.BuildRef(func(row *types.Page[te]) {
				row.Total = 1
			}), nil
		},
		Create: func(_ context.Context, c *storeClient, v *te) (*te, error) {
			ran[textCreateTitle] = c.name

			return v, nil
		},
		Update: func(_ context.Context, c *storeClient, _ string, v *te) (*te, error) {
			ran["Update"] = c.name

			return v, nil
		},
		Delete: func(_ context.Context, c *storeClient, _ string) error {
			ran["Delete"] = c.name

			return nil
		},
		Exists: func(_ context.Context, c *storeClient, _ string) (bool, error) {
			ran["Exists"] = c.name

			return true, nil
		},
		Insert: func(_ context.Context, c *storeClient, _ *te) error {
			ran["Insert"] = c.name

			return nil
		},
		Upsert: func(_ context.Context, c *storeClient, _ *te) error {
			ran["Upsert"] = c.name

			return nil
		},
		PurgeBefore: func(_ context.Context, c *storeClient, _ time.Time) (int, error) {
			ran["PurgeBefore"] = c.name

			return widgetFuncsResult, nil
		},
	}
}

// widgetStoreImpl is a store that embeds the store-tier entity base and adds its product
// query, as a consumer's store does.
type widgetStoreImpl struct {
	store.Entity[*storeClient, te, tp, string]
}

// CountByOwner counts an owner's widgets on the statement's client.
func (s *widgetStoreImpl) CountByOwner(ctx context.Context, _ string) (int, error) {
	return s.Exec(ctx, "count_by_owner", func(ctx context.Context) (int, error) {
		if s.Client(ctx).name == "tx" {
			return 2, nil
		}

		return 1, nil
	})
}

// TestStoreEntity_ServesEveryRoleOnTheStatementsClientAndWritesOnlyInATransaction tests
// the store tier's entity base.
//
// Why this test is important:
//   - A store is its entity base plus its product queries; the base alone decides which
//     client a statement runs on and whether a write may run, so a read on the wrong
//     client misses the transaction's own writes, and a write outside a transaction
//     autocommits half a change.
//
// What it tests:
//   - A type embedding the base satisfies its contract, product query included.
//   - Reads run on the raw client outside a transaction; writes run on the bound client
//     inside one, and fail INTERNAL without running their statement outside one.
func TestStoreEntity_ServesEveryRoleOnTheStatementsClientAndWritesOnlyInATransaction(
	t *testing.T,
) {
	t.Parallel()

	ran := map[string]string{}
	currentWidgetStoreImpl := &widgetStoreImpl{
		Entity: fixtures.Must(store.NewEntity[widgetStore](
			fixtures.Decorations(),
			textWidgets,
			&storeClient{name: textRaw},
			widgetFuncs(ran),
		)),
	}

	var _ widgetStore = currentWidgetStoreImpl

	inTx := store.WithClient(interfaces.WithInTx(t.Context()), &storeClient{name: "tx"})
	callEveryWidgetRole(inTx, t, currentWidgetStoreImpl)

	n, err := currentWidgetStoreImpl.CountByOwner(inTx, "ann")
	require.NoError(t, err)
	assert.Equal(t, wantNATransaction, n)

	for role, client := range ran {
		assert.Equal(t, "tx", client, role)
	}

	clear(ran)

	_, err = currentWidgetStoreImpl.Get(t.Context(), textW1)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{textGetTitle: textRaw}, ran)

	clear(ran)

	_, err = currentWidgetStoreImpl.Create(t.Context(), new(te))
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Equal(
		t,
		apperr.CodeInternal,
		apperr.Code(currentWidgetStoreImpl.Insert(t.Context(), new(te))),
	)

	_, err = currentWidgetStoreImpl.PurgeBefore(t.Context(), time.Now())
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Empty(t, ran, "no write statement runs outside a transaction")
}

// TestEntityBases_ServeOnlyTheirContractsRoles tests the entity bases' construction.
//
// Why this test is important:
//   - A component whose contract names a role the base cannot serve (no statement, or a
//     next tier without the method) would fail only when that role is first called in
//     production; the composition root must fail instead. A narrower contract must
//     still build from fewer statements.
//
// What it tests:
//   - A read-only store contract builds from only Get, List and Exists.
//   - A store contract with a role that has no statement fails INTERNAL naming it.
//   - A repository over a next tier lacking a contract role fails INTERNAL naming it.
//   - A role outside the contract is refused at call time (CodeInternal).
func TestEntityBases_ServeOnlyTheirContractsRoles(t *testing.T) {
	t.Parallel()

	ran := map[string]string{}
	all := widgetFuncs(ran)
	reads := fixtures.Build(func(row *store.Funcs[*storeClient, te, tp, string]) {
		row.Get = all.Get
		row.List = all.List
		row.Exists = all.Exists
	})
	entity, err := store.NewEntity[widgetReads](
		fixtures.Decorations(), textWidgets, &storeClient{name: textRaw}, reads,
	)
	require.NoError(t, err)

	_, err = entity.List(t.Context(), tp{}, stdx.Zero[types.PageRequest]())
	require.NoError(t, err)

	_, err = entity.Create(
		store.WithClient(interfaces.WithInTx(t.Context()), new(storeClient)),
		new(te),
	)
	assert.Equal(
		t,
		apperr.CodeInternal,
		apperr.Code(err),
		"Create is not a widgetReads role",
	)

	_, err = store.NewEntity[widgetStore](
		fixtures.Decorations(), textWidgets, &storeClient{name: textRaw}, reads,
	)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Contains(t, err.Error(), textCreateTitle)

	readOnlyNext := mocks.NewMockReadListExistStore[te, tp, string](
		gomock.NewController(t),
	)

	_, err = repository.NewEntity[interfaces.Repository[te, tp, string], te, tp, string](
		fixtures.Decorations(), textWidgets, readOnlyNext, nil,
	)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Contains(t, err.Error(), textCreateTitle)
}

// TestRepositoryEntity_RetriesReadsAndIdempotentWritesOnly tests the repository tier's
// entity base.
//
// Why this test is important:
//   - A repository is where a store call is retried; a retried create whose first
//     attempt landed inserts a duplicate, and a retried append or upsert inside the
//     caller's transaction would run after the transaction already failed.
//
// What it tests:
//   - Every role reaches the next tier once and returns its result.
//   - Get, List, Update, Delete, Exists and PurgeBefore go through the retrier (six
//     calls); Create, Insert and Upsert do not.
//   - Next returns the next tier as given, for a product query.
func TestRepositoryEntity_RetriesReadsAndIdempotentWritesOnly(t *testing.T) {
	t.Parallel()

	next := newWidgetNext(gomock.NewController(t))
	expectEveryWidgetRole(next)

	repo := fixtures.Must(repository.NewEntity[widgetStore, te, tp, string](
		decorate.Fixed(
			fixtures.BuildRef(func(row *decorate.Deps) {
				row.Retrier = passThroughRetrier(t, 6)
			}),
		),
		textWidgets,
		next,
		nil,
	))
	callEveryWidgetRole(t.Context(), t, repo)
	assert.Equal(t, next, repo.Next())
}

// TestServiceEntity_ValidatesWritesBeforeTheRepository tests the service tier's entity
// base.
//
// Why this test is important:
//   - A service is where a write's entity is checked; an invalid entity that reached the
//     repository would persist bad data.
//
// What it tests:
//   - Every role reaches the repository and returns its result.
//   - A Create, Update or Upsert whose entity fails the validator returns its error and
//     never reaches the repository (the strict mock allows no further call).
func TestServiceEntity_ValidatesWritesBeforeTheRepository(t *testing.T) {
	t.Parallel()

	next := newWidgetNext(gomock.NewController(t))
	expectEveryWidgetRole(next)

	rejectEmpty := func(v *te) error {
		if v.ID == "" {
			return apperr.InvalidInput("id is required")
		}

		return nil
	}
	svc := fixtures.Must(service.NewEntity[widgetStore, te, tp, string](
		fixtures.Decorations(), textWidgets, next, rejectEmpty,
	))
	callEveryWidgetRole(t.Context(), t, svc)

	_, err := svc.Create(t.Context(), new(te))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = svc.Update(t.Context(), textW1, new(te))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Equal(
		t,
		apperr.CodeInvalidInput,
		apperr.Code(svc.Upsert(t.Context(), new(te))),
	)
}

// TestPipelineEntity_RunsEveryRoleOverTheService tests the pipeline tier's entity base.
//
// Why this test is important:
//   - A CRUD pipeline is its entity base and nothing else; a dropped or misrouted role
//     breaks every workflow and handler above it.
//
// What it tests:
//   - Every role of a service-shaped contract reaches the service once and returns its
//     result, outside any transaction.
func TestPipelineEntity_RunsEveryRoleOverTheService(t *testing.T) {
	t.Parallel()

	svc := mocks.NewMockService[te, tp, string](gomock.NewController(t))
	testEntity := &te{ID: textW1, Name: ""}
	notInTx := gomock.Cond(
		func(ctx context.Context) bool { return !interfaces.InTx(ctx) },
	)
	svc.EXPECT().Get(notInTx, textW1).Return(testEntity, nil)
	svc.EXPECT().List(notInTx, tp{}, gomock.Any()).Return(new(types.Page[te]), nil)
	svc.EXPECT().Create(notInTx, testEntity).Return(testEntity, nil)
	svc.EXPECT().Update(notInTx, textW1, testEntity).Return(testEntity, nil)
	svc.EXPECT().Delete(notInTx, textW1).Return(nil)

	pipe := fixtures.Must(pipeline.NewEntity[widgetService, te, tp, string](
		fixtures.Decorations(), textWidgets, svc,
	))

	var _ widgetService = pipe

	_, err := pipe.Get(t.Context(), textW1)
	require.NoError(t, err)

	_, err = pipe.List(t.Context(), tp{}, stdx.Zero[types.PageRequest]())
	require.NoError(t, err)

	_, err = pipe.Create(t.Context(), testEntity)
	require.NoError(t, err)

	_, err = pipe.Update(t.Context(), textW1, testEntity)
	require.NoError(t, err)
	require.NoError(t, pipe.Delete(t.Context(), textW1))
}

// TestWorkflowEntity_OpensOneTransactionPerWriteAndNoneForARead tests the workflow
// tier's entity base.
//
// Why this test is important:
//   - The workflow is the only tier that opens a transaction; a write outside one is
//     refused by every store below, and a read inside one holds a connection for nothing.
//
// What it tests:
//   - Create, Update and Delete each run in their own transaction (three, on a context
//     the pipeline sees as in a transaction); Get and List run with none.
func TestWorkflowEntity_OpensOneTransactionPerWriteAndNoneForARead(t *testing.T) {
	t.Parallel()

	pipes := mocks.NewMockService[te, tp, string](gomock.NewController(t))
	testEntity := &te{ID: textW1, Name: ""}
	matcher := gomock.Cond(interfaces.InTx)
	out := gomock.Cond(func(ctx context.Context) bool { return !interfaces.InTx(ctx) })
	pipes.EXPECT().Get(out, textW1).Return(testEntity, nil)
	pipes.EXPECT().List(out, tp{}, gomock.Any()).Return(new(types.Page[te]), nil)
	pipes.EXPECT().Create(matcher, testEntity).Return(testEntity, nil)
	pipes.EXPECT().Update(matcher, textW1, testEntity).Return(testEntity, nil)
	pipes.EXPECT().Delete(matcher, textW1).Return(nil)

	flow := fixtures.Must(workflow.NewEntity[widgetService, te, tp, string](
		fixtures.Decorations(), textWidgets, txManager(t, txManagerInput, nil), pipes,
	))

	_, err := flow.Get(t.Context(), textW1)
	require.NoError(t, err)

	_, err = flow.List(t.Context(), tp{}, stdx.Zero[types.PageRequest]())
	require.NoError(t, err)

	_, err = flow.Create(t.Context(), testEntity)
	require.NoError(t, err)

	_, err = flow.Update(t.Context(), textW1, testEntity)
	require.NoError(t, err)
	require.NoError(t, flow.Delete(t.Context(), textW1))
}

const (
	// returnInputRole is the return input role the tests use.
	returnInputRole = 3

	// txManagerInput is the tx manager input the tests use.
	txManagerInput = 3

	// wantNATransaction is the expected natransaction.
	wantNATransaction = 2

	// wantNRole is the expected nrole.
	wantNRole = 3

	// widgetFuncsResult is the widget funcs result the tests use.
	widgetFuncsResult = 3
)

const (
	// textCreateTitle is the literal "Create".
	textCreateTitle = "Create"
)
