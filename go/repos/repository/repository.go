// Package repository is the repository tier: a Base is the embeddable core of a
// consumer's repository, running its custom queries through the tier's decoration
// (Tier) with the promoted Exec, and building the decorated CRUD repositories the tier
// offers over a store. Every repository it builds is a core interface. Cached adds the
// read-through entity cache beneath them; DecoratedFromConfig assembles both.
package repository

import (
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
// custom query through the tier's decoration. The zero Base runs queries undecorated.
type Base struct {
	decorate.Subject

	// create runs a non-idempotent write: the decoration without retry.
	create decorate.Subject

	// insert runs an append inside the caller's transaction: observability only, since
	// a retry, breaker or timeout would corrupt the transaction (the tier orders no
	// recovery or auth).
	insert decorate.Subject
}

// NewBase builds the Base for the repository named name, decorated with deps.
func NewBase(name string, deps decorate.Deps) Base {
	return Base{
		Subject: Tier().Subject(name, deps),
		create:  Tier().Subject(name, deps.WithoutRetry()),
		insert:  Tier().Subject(name, decorate.Deps{Observability: deps.Observability}),
	}
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
