package repository

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

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
		decorate.NewWriter(b.Once(), b.Subject, store, nil),
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
		decorate.ResourceOps{Create: b.Once(), Rest: b.Subject},
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
		decorate.NewWriter(b.Once(), b.Subject, store, nil),
		decorate.NewDeleter(b.Subject, store),
		decorate.NewExister(b.Subject, store),
	}
}

// Outbox is the decorated outbox repository over store. Claim runs without retry: it
// leases rows and counts an attempt, so a second attempt would lease another batch. The
// finalizers (guarded on the row still pending), Stats and DeleteSentBefore are
// idempotent and run through the full chain.
func (b Base) Outbox(store interfaces.OutboxStore) interfaces.OutboxRepository {
	return decorate.NewOutbox(b.Once(), b.Subject, store)
}

// TxSafeInsert is the decorated append-only repository over store, whose Insert runs
// inside the caller's transaction: it is observed but never retried, broken or timed
// out, so a transactional-outbox or audit row commits with its transaction.
func (b Base) TxSafeInsert[T any](
	store interfaces.InsertOnlyStore[T],
) interfaces.InsertOnlyStore[T] {
	return decorate.NewInserter(b.Observed(), store)
}
