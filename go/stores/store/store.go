// Package store is the store tier: the persistence layer a repository is built on. A
// store issues one entity's statements on its ORM's client — which ORM is the store's
// Kind (Ent, GORM, …), one subpackage per kind under stores/ — and the repository
// above it adds caching and resilience.
//
// Tier orders the store tier's layers, switched and tuned per store by the decoration
// config's stores tier (decoration.stores, built by decorate.Decorations; unobserved by
// default, so a request is traced once, at its workflow and clients). Base is the
// embeddable core of a store, built from its contract: its statements run through the
// tier on the request transaction's client when a transaction manager bound one with
// WithClient, else on the store's own. An Entity (funcs.go) is a Base that also serves an
// entity's roles from one statement per role.
package store

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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

// Base is the embeddable core of a store named by its Subject over a client of type C (an
// *ent.Client, a *gorm.DB): Name and Exec run a read through the store tier, Write runs a
// write the same way but only inside a transaction, and Client returns the client it runs
// on. Each operation is a method of the store's contract, decorated under its own name.
// The zero Base runs statements undecorated on the zero client.
type Base[C any] struct {
	// raw is the client used outside a transaction.
	raw C

	// Subject runs the store's operations through the tier; it supplies Name and Exec.
	decorate.Subject
}

// NewBase builds the Base for the store named name over raw, the client used outside a
// transaction, whose operations are the methods of its contract S (an interface): the
// store is decorated from decs as the store-tier component name, and each operation op as
// name+"."+op. A contract that is not an interface, or a decoration that fails to
// resolve, is an error.
func NewBase[S, C any](decs decorate.Decorations, name string, raw C) (Base[C], error) {
	subject, err := decs.SubjectOf[S](Tier(), name)
	if err != nil {
		return Base[C]{}, err
	}
	return Base[C]{raw: raw, Subject: subject}, nil
}

// Client returns the client a statement on ctx runs on: the transaction-bound one, if
// any, else the store's own.
func (b Base[C]) Client(ctx context.Context) C {
	if c, ok := ClientFrom[C](ctx); ok {
		return c
	}
	return b.raw
}

// Write runs fn as the write op through the store tier, only inside a transaction: on a
// context with no transaction it fails INTERNAL without running fn, so a write never
// autocommits outside the workflow that owns it. Reads run through Exec.
func (b Base[C]) Write[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	if !interfaces.InTx(ctx) {
		var zero R
		return zero, apperr.New(
			apperr.CodeInternal,
			"store "+b.Name()+": "+op+" writes outside a transaction",
		)
	}
	return b.Exec(ctx, op, fn)
}
