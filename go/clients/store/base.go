// Package store provides the embeddable core of a consumer's persistence store: the
// database client a store's statements run on, bound to the request's transaction when
// one is open. A transaction manager binds its transaction's client to the context with
// WithClient; a store embeds Base and runs every statement on b.Client(ctx).
package store

import "context"

// clientKey is the context key a client of type C is bound under; each C gets its own.
type clientKey[C any] struct{}

// WithClient returns ctx carrying c as the transaction-bound client of type C.
func WithClient[C any](ctx context.Context, c C) context.Context {
	return context.WithValue(ctx, clientKey[C]{}, c)
}

// ClientFrom returns the client of type C bound to ctx, and whether one is.
func ClientFrom[C any](ctx context.Context) (C, bool) {
	c, ok := ctx.Value(clientKey[C]{}).(C)
	return c, ok
}

// Base is the embeddable core of a store over a client of type C (an *ent.Client, a
// *sql.DB): Client returns the transaction's client when one is bound to the context,
// else the store's own.
type Base[C any] struct {
	// raw is the client used outside a transaction.
	raw C
}

// NewBase builds the Base over raw, the client used outside a transaction.
func NewBase[C any](raw C) Base[C] {
	return Base[C]{raw: raw}
}

// Client returns the client a statement on ctx runs on: the transaction-bound one, if
// any, else the store's own.
func (b Base[C]) Client(ctx context.Context) C {
	if c, ok := ClientFrom[C](ctx); ok {
		return c
	}
	return b.raw
}
