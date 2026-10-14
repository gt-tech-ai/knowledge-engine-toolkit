// Package store is the store tier: the persistence layer a repository is built on. A
// store issues one entity's statements on its ORM's client — which ORM is the store's
// Kind (Ent, GORM, …), one subpackage per kind under stores/ — and the repository
// above it adds caching and resilience.
//
// Tier orders the store tier's layers, switched and tuned per store by the decoration
// config's stores tier (decoration.stores, built by decorate.Decorations; unobserved by
// default, so a request is traced once, at its workflow and clients). Decorate puts any
// interfaces.Store behind the tier; Base is the embeddable core of a hand-written
// store: its statements run through the tier on the request transaction's client when a
// transaction manager bound one with WithClient, else on the store's own.
package store

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindEnt builds stores on Ent (stores/ent: the list-query compiler and folds).
	KindEnt = "ent"

	// KindGORM builds stores on GORM (stores/gorm: the lifecycle-managed client).
	KindGORM = "gorm"
)

// Tier is the store decoration tier, outermost first: each statement gets its span,
// metrics and log line, then its deadline. Retry and the breaker belong to the
// repository above, which knows whether the statement runs in a transaction.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "store", Order: []decorate.Layer{
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerTimeout,
	}}
}

// Decorate returns s with every operation run through the store tier as the store named
// name, decorated with deps.
func Decorate[T, P any, ID comparable](
	s interfaces.Store[T, P, ID],
	name string,
	deps decorate.Deps,
) interfaces.Store[T, P, ID] {
	op := Tier().Subject(name, deps)
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
		decorate.Exister[ID]
	}{
		decorate.NewReader(op, s),
		decorate.NewLister(op, s),
		decorate.NewWriter(op, op, s, nil),
		decorate.NewDeleter(op, s),
		decorate.NewExister(op, s),
	}
}

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

// Base is the embeddable core of a store named by its Subject over a client of type C
// (an *ent.Client, a *gorm.DB): Name and Exec run a statement through the store tier,
// and Client returns the client it runs on. The zero Base runs statements undecorated
// on the zero client.
type Base[C any] struct {
	// raw is the client used outside a transaction.
	raw C

	// Subject runs the store's statements through the tier; it supplies Name and Exec.
	decorate.Subject
}

// NewBase builds the Base for the store named name over raw, the client used outside a
// transaction, decorated with deps.
func NewBase[C any](name string, raw C, deps decorate.Deps) Base[C] {
	return Base[C]{raw: raw, Subject: Tier().Subject(name, deps)}
}

// Client returns the client a statement on ctx runs on: the transaction-bound one, if
// any, else the store's own.
func (b Base[C]) Client(ctx context.Context) C {
	if c, ok := ClientFrom[C](ctx); ok {
		return c
	}
	return b.raw
}
