package unit_test

import (
	"context"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// storeClient stands in for a store's database client (an *ent.Client, a *sql.DB).
type storeClient struct {
	// name tells the raw client from the transaction's.
	name string
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
	raw, tx := &storeClient{name: "raw"}, &storeClient{name: "tx"}
	base := store.NewBase("documents", raw, decorate.Deps{})

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
	for _, mf := range mfs {
		if mf.GetName() != "store_operations_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "operation" && l.GetValue() == op {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
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
	base := store.NewBase("documents", &storeClient{name: "raw"}, decorate.Deps{})
	write := func(context.Context) (int, error) { return 1, nil }

	got, err := base.Write(interfaces.WithInTx(t.Context()), "Create", write)
	require.NoError(t, err)
	assert.Equal(t, 1, got)

	_, err = base.Write(t.Context(), "Create", func(context.Context) (int, error) {
		t.Error("a write outside a transaction must not run")
		return 0, nil
	})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	got, err = base.Exec(t.Context(), "Get", write)
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
//     on, a Decorate'd store and a Base's Exec each count their operation once.
func TestStoreTier_DecoratesFromTheStoresTier(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "store", Order: []decorate.Layer{
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerTimeout,
	}}, store.Tier())

	reg := promclient.NewRegistry()
	collab := decorate.Collaborators{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
	}
	defaults := decoration.DefaultConfig()
	unobserved, err := decorate.NewDecorations(&defaults, collab).
		Deps(store.Tier(), "documents")
	require.NoError(t, err)
	assert.Nil(t, unobserved.Metrics)

	on := true
	cfg := decoration.DefaultConfig()
	cfg.Stores.Defaults.Metrics = &on
	deps, err := decorate.NewDecorations(&cfg, collab).Deps(store.Tier(), "documents")
	require.NoError(t, err)

	inner := mocks.NewMockStore[reposEnt, reposEntParams, string](gomock.NewController(t))
	inner.EXPECT().Get(gomock.Any(), "a").Return(&reposEnt{ID: "a"}, nil)
	got, err := store.Decorate(inner, "documents", deps).Get(t.Context(), "a")
	require.NoError(t, err)
	assert.Equal(t, "a", got.ID)

	base := store.NewBase("documents", &storeClient{name: "raw"}, deps)
	n, err := base.Exec(
		t.Context(),
		"CountByWorkspace",
		func(context.Context) (int, error) {
			return 3, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	assert.InDelta(t, 1, storeOpCounter(t, reg, "Get"), 0)
	assert.InDelta(t, 1, storeOpCounter(t, reg, "CountByWorkspace"), 0)
}
