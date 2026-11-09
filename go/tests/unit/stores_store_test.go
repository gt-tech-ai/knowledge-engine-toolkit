package unit_test

import (
	"context"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// storeClient stands in for a store's database client (an *ent.Client, a *sql.DB).
type storeClient struct {
	// name tells the raw client from the transaction's.
	name string
}

// documentsStore is the test store's contract: CRUD plus one custom query.
type documentsStore interface {
	interfaces.Store[reposEnt, reposEntParams, string]

	// CountByWorkspace counts the workspace's documents.
	CountByWorkspace(ctx context.Context, workspace string) (int, error)
}

// documentsReads is a store contract with one role and one custom query.
type documentsReads interface {
	interfaces.Reader[reposEnt, string]

	// CountByWorkspace counts the workspace's documents.
	CountByWorkspace(ctx context.Context, workspace string) (int, error)
}

// TestStoreBase_UsesTheTransactionsClientWhenOneIsBound tests the embeddable store
// base's client lookup.
//
// Why this test is important:
//   - A store's statements must join the transaction the request workflow opened; a
//     store that used its raw client inside a transaction would commit half a change
//     on its own and see none of the transaction's uncommitted writes.
//
// What it tests:
//   - Outside a transaction Client returns the raw client.
//   - With a client of the store's type bound to ctx, Client returns that one.
//   - A bound client of another type is ignored.
func TestStoreBase_UsesTheTransactionsClientWhenOneIsBound(t *testing.T) {
	t.Parallel()

	raw, tx := &storeClient{name: textRaw}, &storeClient{name: "tx"}
	base := fixtures.Must(
		store.NewBase[documentsStore](fixtures.Decorations(), textDocuments, raw),
	)

	assert.Same(t, raw, base.Client(t.Context()))

	inTx := store.WithClient(t.Context(), tx)
	assert.Same(t, tx, base.Client(inTx))

	other := store.WithClient(context.Background(), "not a storeClient")
	assert.Same(t, raw, base.Client(other))

	got, ok := store.ClientFrom[*storeClient](inTx)
	assert.True(t, ok)
	assert.Same(t, tx, got)
}

// storeOpCounter returns store_operations_total for the operation op in reg.
func storeOpCounter(t *testing.T, reg *promclient.Registry, op string) float64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	v, _ := counterByLabel(mfs, "store_operations_total", "operation", op)

	return v
}

// TestStoreBaseWrite_RunsOnlyInsideATransaction tests the store base's write guard.
//
// Why this test is important:
//   - A store write outside a transaction autocommits on its own: a write that escaped
//     its workflow would commit half a change and nothing would report it.
//
// What it tests:
//   - Write on a context in a transaction runs fn and returns its result.
//   - Write on a context with no transaction fails INTERNAL without running fn.
//   - Exec (a read) runs on either.
func TestStoreBaseWrite_RunsOnlyInsideATransaction(t *testing.T) {
	t.Parallel()

	base := fixtures.Must(store.NewBase[documentsStore](
		fixtures.Decorations(), textDocuments, &storeClient{name: textRaw},
	))
	write := func(context.Context) (int, error) { return 1, nil }

	got, err := base.Write(interfaces.WithInTx(t.Context()), textCreate, write)
	require.NoError(t, err)
	assert.Equal(t, 1, got)

	_, err = base.Write(t.Context(), textCreate, func(context.Context) (int, error) {
		t.Error("a write outside a transaction must not run")

		return 0, nil
	})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	got, err = base.Exec(t.Context(), textGet, write)
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

// TestStoreTier_DecoratesFromTheStoresTier tests the store decoration tier.
//
// Why this test is important:
//   - A store's statements are observed only when the decoration config's stores tier
//     turns that on (off by default, so a request is not traced at every tier); a
//     store tier that ignored the config, or put retry beneath the repository's, would
//     double-observe every request or replay a statement inside a transaction.
//
// What it tests:
//   - The tier is named "store" and orders tracing → metrics → logging → timeout.
//   - Under the default config a store is unobserved; with the stores tier's metrics
//     on, a Base's CRUD role and its Exec each count their operation once.
func TestStoreTier_DecoratesFromTheStoresTier(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: textStore, Order: []decorate.Layer{
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerTimeout,
	}}, store.Tier())

	reg := promclient.NewRegistry()
	collab := decorate.Collaborators{
		Metrics: prom.NewFromRegistry(reg),
	}
	defaults := decoration.DefaultConfig()
	unobserved, err := decorate.NewDecorations(&defaults, collab).
		Deps(store.Tier(), textDocuments)
	require.NoError(t, err)
	assert.Nil(t, unobserved.Metrics)

	on := true
	cfg := decoration.DefaultConfig()

	cfg.Stores.Defaults.Metrics = &on

	decs := decorate.NewDecorations(&cfg, collab)
	docs, err := store.NewEntity[documentsReads](
		decs,
		textDocuments,
		&storeClient{name: textRaw},
		store.Funcs[*storeClient, reposEnt, reposEntParams, string]{
			Get: func(_ context.Context, _ *storeClient, id string) (*reposEnt, error) {
				return &reposEnt{ID: id}, nil
			},
		},
	)
	require.NoError(t, err)

	got, err := docs.Get(t.Context(), textA)
	require.NoError(t, err)
	assert.Equal(t, textA, got.ID)

	n, err := docs.Exec(
		t.Context(),
		"count_by_workspace",
		func(context.Context) (int, error) {
			return 3, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, equalArg3, n)

	assert.InDelta(t, 1, storeOpCounter(t, reg, textGet), 0)
	assert.InDelta(t, 1, storeOpCounter(t, reg, "count_by_workspace"), 0)
}
