package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/store"
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
	base := store.NewBase(raw)

	assert.Same(t, raw, base.Client(t.Context()))

	inTx := store.WithClient(t.Context(), tx)
	assert.Same(t, tx, base.Client(inTx))

	other := store.WithClient(context.Background(), "not a storeClient")
	assert.Same(t, raw, base.Client(other))
	got, ok := store.ClientFrom[*storeClient](inTx)
	assert.True(t, ok)
	assert.Same(t, tx, got)
}
