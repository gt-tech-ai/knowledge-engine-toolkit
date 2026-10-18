// Package repository is the repository tier: a Base is the embeddable core of a
// consumer's repository, running its custom queries through the tier's decoration
// (Tier) with the promoted Exec, and building the decorated CRUD repositories the tier
// offers over a store. Every repository it builds is a core interface. Cached adds the
// read-through entity cache beneath them; DecoratedFromConfig assembles both.
package repository

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the repository tier's decoration: tracing → metrics → logging → timeout →
// circuit breaker → retry, outermost first. Retry never runs inside a transaction,
// and a Create never retries (it is not idempotent).
func Tier() decorate.Tier {
	return decorate.Tier{Name: "repository", Order: []decorate.Layer{
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerTimeout,
		decorate.LayerCircuitBreaker,
		decorate.LayerRetry,
	}}
}

// Base is the embeddable core of a repository named by its Subject: Name and Exec run a
// custom query through the tier's decoration, and Query and Command build its named
// operations, each under its own decoration name. The zero Base runs queries undecorated.
type Base struct {
	decorate.Subject

	// create runs a non-idempotent write: the decoration without retry.
	create decorate.Subject

	// insert runs an append inside the caller's transaction: observability only, since
	// a retry, breaker or timeout would corrupt the transaction (the tier orders no
	// recovery or auth).
	insert decorate.Subject

	// ops resolves each named operation's decoration beneath the repository's name.
	ops decorate.Scope
}

// NewBase builds the Base for the repository named name, decorated with deps; its
// named operations share deps.
func NewBase(name string, deps decorate.Deps) Base {
	return Base{
		Subject: Tier().Subject(name, deps),
		ops:     decorate.NewScope(Tier(), name, deps),
		create:  Tier().Subject(name, deps.WithoutRetry()),
		insert:  Tier().Subject(name, decorate.Deps{Observability: deps.Observability}),
	}
}

// NewBaseFrom builds the Base for the repository named name, decorated from decs: the
// repository as the repository-tier component name, and each named operation op as
// name+"."+op. A decoration that fails to resolve is CodeInvalidInput.
func NewBaseFrom(decs decorate.Decorations, name string) (Base, error) {
	deps, err := decs.Deps(Tier(), name)
	if err != nil {
		return Base{}, err
	}
	b := NewBase(name, deps)
	b.ops = decs.Scope(Tier(), name)
	return b, nil
}

// Query is the repository's named read op, run through the repository tier with its
// retry: a read is idempotent.
func Query[In, Out any](
	b Base,
	op string,
	fn func(ctx context.Context, in In) (Out, error),
) decorate.OpFunc[In, Out] {
	return decorate.Operation(b.ops.Subject(op), fn)
}

// Command is the repository's named write op, run through the repository tier without
// retry: a write whose first attempt committed must not run twice.
func Command[In, Out any](
	b Base,
	op string,
	fn func(ctx context.Context, in In) (Out, error),
) decorate.OpFunc[In, Out] {
	return decorate.Operation(b.ops.Subject(op, decorate.Deps.WithoutRetry), fn)
}

// CRUD is the decorated repository over store.
func (b Base) CRUD[T, P any, ID comparable](
	store interfaces.Store[T, P, ID],
) interfaces.Repository[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, store),
		decorate.NewLister(b.Subject, store),
		decorate.NewWriter(b.create, b.Subject, store, nil),
		decorate.NewDeleter(b.Subject, store),
		decorate.NewExister(b.Subject, store),
	}
}

// Resource is the decorated repository over store, a resource whose Create takes C and
// whose Update takes U: every role runs through the repository's decoration, and Create
// never retries.
func (b Base) Resource[T, P any, ID comparable, C, U any](
	store interfaces.Resource[T, P, ID, C, U],
) interfaces.Resource[T, P, ID, C, U] {
	return decorate.NewResource[T, P, ID, C, U](
		decorate.ResourceOps{Create: b.create, Rest: b.Subject},
		store,
		nil,
		nil,
	)
}

// ReadListExist is the decorated read-only repository over store, for a resource
// whose writes are owned elsewhere.
func (b Base) ReadListExist[T, P any, ID comparable](
	store interfaces.ReadListExistStore[T, P, ID],
) interfaces.ReadListExistStore[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, store),
		decorate.NewLister(b.Subject, store),
		decorate.NewExister(b.Subject, store),
	}
}

// CRUDNoList is the decorated repository over store for a resource with no listable
// collection.
func (b Base) CRUDNoList[T any, ID comparable](
	store interfaces.CRUDNoListStore[T, ID],
) interfaces.CRUDNoListStore[T, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, store),
		decorate.NewWriter(b.create, b.Subject, store, nil),
		decorate.NewDeleter(b.Subject, store),
		decorate.NewExister(b.Subject, store),
	}
}

// TxSafeInsert is the decorated append-only repository over store, whose Insert runs
// inside the caller's transaction: it is observed but never retried, broken or timed
// out, so a transactional-outbox or audit row commits with its transaction.
func (b Base) TxSafeInsert[T any](
	store interfaces.InsertOnlyStore[T],
) interfaces.InsertOnlyStore[T] {
	return decorate.NewInserter(b.insert, store)
}
