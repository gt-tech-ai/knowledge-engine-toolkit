package store

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// CRUD is the decorated store over impl: every operation runs through the store tier.
func (b Base[C]) CRUD[T, P any, ID comparable](
	impl interfaces.Store[T, P, ID],
) interfaces.Store[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, impl),
		decorate.NewLister(b.Subject, impl),
		decorate.NewWriter(b.Subject, b.Subject, impl, nil),
		decorate.NewDeleter(b.Subject, impl),
		decorate.NewExister(b.Subject, impl),
	}
}

// CRUDNoList is CRUD for a resource with no listable collection.
func (b Base[C]) CRUDNoList[T any, ID comparable](
	impl interfaces.CRUDNoListStore[T, ID],
) interfaces.CRUDNoListStore[T, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, impl),
		decorate.NewWriter(b.Subject, b.Subject, impl, nil),
		decorate.NewDeleter(b.Subject, impl),
		decorate.NewExister(b.Subject, impl),
	}
}

// ReadListExist is the decorated read-only store over impl.
func (b Base[C]) ReadListExist[T, P any, ID comparable](
	impl interfaces.ReadListExistStore[T, P, ID],
) interfaces.ReadListExistStore[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, impl),
		decorate.NewLister(b.Subject, impl),
		decorate.NewExister(b.Subject, impl),
	}
}

// Resource is the decorated store over impl, a resource whose Create takes CIn and whose
// Update takes UIn.
func (b Base[C]) Resource[T, P any, ID comparable, CIn, UIn any](
	impl interfaces.Resource[T, P, ID, CIn, UIn],
) interfaces.Resource[T, P, ID, CIn, UIn] {
	return decorate.NewResource[T, P, ID, CIn, UIn](
		decorate.ResourceOps{Create: b.Subject, Rest: b.Subject},
		impl,
		nil,
		nil,
	)
}

// Outbox is the decorated outbox store over impl: every operation runs through the store
// tier.
func (b Base[C]) Outbox(impl interfaces.OutboxStore) interfaces.OutboxStore {
	return decorate.NewOutbox(b.Subject, b.Subject, impl)
}
