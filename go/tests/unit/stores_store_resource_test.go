package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// resourceCreate is the create input of the test resource, a command rather than the
// entity itself.
type resourceCreate struct {
	// Name is the new entity's name.
	Name string
}

// resourceFuncs are the test resource's store statements.
type resourceFuncs = store.Funcs[
	*storeClient, reposEnt, reposEntParams, string, resourceCreate, *reposEnt,
]

// resourceAPI is the test resource's interfaces.Resource.
type resourceAPI = interfaces.Resource[
	reposEnt, reposEntParams, string, resourceCreate, *reposEnt,
]

// resourceStore is the store-tier Resource of the test resource.
type resourceStore = store.Resource[
	*storeClient, reposEnt, reposEntParams, string, resourceCreate, *reposEnt,
]

// newResourceStore builds the test resource's store over raw, recording the client each
// statement ran on in ran under the statement's role.
func newResourceStore(
	t *testing.T,
	raw *storeClient,
	ran map[string]string,
) resourceStore {
	t.Helper()
	res, err := store.NewResource(
		store.NewBase("things", raw, decorate.Deps{}),
		resourceFuncs{
			Get: func(_ context.Context, c *storeClient, id string) (*reposEnt, error) {
				ran["Get"] = c.name
				return &reposEnt{ID: id}, nil
			},
			List: func(
				_ context.Context,
				c *storeClient,
				_ reposEntParams,
				_ types.PageRequest,
			) (*types.Page[reposEnt], error) {
				ran["List"] = c.name
				return &types.Page[reposEnt]{Items: []reposEnt{{ID: "a"}}}, nil
			},
			Create: func(_ context.Context, c *storeClient, in resourceCreate) (*reposEnt, error) {
				ran["Create"] = c.name
				return &reposEnt{ID: in.Name}, nil
			},
			Update: func(
				_ context.Context,
				c *storeClient,
				id string,
				_ *reposEnt,
			) (*reposEnt, error) {
				ran["Update"] = c.name
				return &reposEnt{ID: id}, nil
			},
			Delete: func(_ context.Context, c *storeClient, _ string) error {
				ran["Delete"] = c.name
				return nil
			},
			Exists: func(_ context.Context, c *storeClient, _ string) (bool, error) {
				ran["Exists"] = c.name
				return true, nil
			},
		},
	)
	require.NoError(t, err)
	return res
}

// TestStoreResource_RunsEveryRoleOnTheStatementsClientAndWritesOnlyInATransaction tests
// the store tier's Resource base.
//
// Why this test is important:
//   - A resource's store is assembled from the base rather than hand-written, so the
//     base alone decides which client a statement uses and whether a write may run: a
//     read on the wrong client misses the transaction's own writes, and a write outside
//     a transaction autocommits half a change.
//
// What it tests:
//   - The Resource satisfies interfaces.Resource with a command-shaped Create input.
//   - Reads run on the raw client outside a transaction.
//   - Writes run on the transaction-bound client inside one, and fail INTERNAL without
//     running their statement outside one.
func TestStoreResource_RunsEveryRoleOnTheStatementsClientAndWritesOnlyInATransaction(
	t *testing.T,
) {
	t.Parallel()
	ran := map[string]string{}
	res := newResourceStore(t, &storeClient{name: "raw"}, ran)
	var _ resourceAPI = res

	got, err := res.Get(t.Context(), "a")
	require.NoError(t, err)
	assert.Equal(t, "a", got.ID)
	page, err := res.List(t.Context(), reposEntParams{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	ok, err := res.Exists(t.Context(), "a")
	require.NoError(t, err)
	assert.True(t, ok)

	inTx := store.WithClient(interfaces.WithInTx(t.Context()), &storeClient{name: "tx"})
	created, err := res.Create(inTx, resourceCreate{Name: "b"})
	require.NoError(t, err)
	assert.Equal(t, "b", created.ID)
	_, err = res.Update(inTx, "b", &reposEnt{})
	require.NoError(t, err)
	require.NoError(t, res.Delete(inTx, "b"))
	assert.Equal(t, map[string]string{
		"Get": "raw", "List": "raw", "Exists": "raw",
		"Create": "tx", "Update": "tx", "Delete": "tx",
	}, ran)

	clear(ran)
	_, err = res.Create(t.Context(), resourceCreate{Name: "c"})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	_, err = res.Update(t.Context(), "c", &reposEnt{})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Equal(t, apperr.CodeInternal, apperr.Code(res.Delete(t.Context(), "c")))
	assert.Empty(t, ran, "no write statement runs outside a transaction")
}

// TestStoreResource_RefusesAMissingStatement tests the Resource base's construction.
//
// Why this test is important:
//   - A Resource with a role whose statement was never supplied would fail only when
//     that role is first called in production; the composition root must fail instead.
//
// What it tests:
//   - NewResource with any statement missing fails INTERNAL, naming the role.
func TestStoreResource_RefusesAMissingStatement(t *testing.T) {
	t.Parallel()
	_, err := store.NewResource(
		store.NewBase("things", &storeClient{name: "raw"}, decorate.Deps{}),
		resourceFuncs{},
	)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Contains(t, err.Error(), "Get")
}
