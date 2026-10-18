package store

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// GetFunc reads the entity identified by id on c.
type GetFunc[C, T any, ID comparable] func(ctx context.Context, c C, id ID) (*T, error)

// ListFunc reads the page of entities params and page select on c.
type ListFunc[C, T, P any] func(
	ctx context.Context,
	c C,
	params P,
	page types.PageRequest,
) (*types.Page[T], error)

// CreateFunc writes a new entity built from in on c.
type CreateFunc[C, T, In any] func(ctx context.Context, c C, in In) (*T, error)

// UpdateFunc writes in to the entity identified by id on c.
type UpdateFunc[C, T any, ID comparable, In any] func(
	ctx context.Context,
	c C,
	id ID,
	in In,
) (*T, error)

// DeleteFunc removes the entity identified by id on c.
type DeleteFunc[C any, ID comparable] func(ctx context.Context, c C, id ID) error

// ExistsFunc reports whether an entity identified by id exists on c.
type ExistsFunc[C any, ID comparable] func(ctx context.Context, c C, id ID) (bool, error)

// Reader is the store-tier Base of interfaces.Reader: Get runs its statement through
// the tier on the client the context's statement runs on.
type Reader[C, T any, ID comparable] struct {
	// get is the statement.
	get GetFunc[C, T, ID]

	// base runs the statement.
	base Base[C]
}

// NewReader builds the Reader running get through base.
func NewReader[C, T any, ID comparable](
	base Base[C],
	get GetFunc[C, T, ID],
) Reader[C, T, ID] {
	return Reader[C, T, ID]{base: base, get: get}
}

// Get returns the entity identified by id.
func (r Reader[C, T, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return r.base.Exec(ctx, "Get", func(ctx context.Context) (*T, error) {
		return r.get(ctx, r.base.Client(ctx), id)
	})
}

// Lister is the store-tier Base of interfaces.Lister: List runs its statement through
// the tier on the client the context's statement runs on.
type Lister[C, T, P any] struct {
	// list is the statement.
	list ListFunc[C, T, P]

	// base runs the statement.
	base Base[C]
}

// NewLister builds the Lister running list through base.
func NewLister[C, T, P any](
	base Base[C],
	list ListFunc[C, T, P],
) Lister[C, T, P] {
	return Lister[C, T, P]{base: base, list: list}
}

// List returns the page of entities params and page select.
func (l Lister[C, T, P]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return l.base.Exec(ctx, "List", func(ctx context.Context) (*types.Page[T], error) {
		return l.list(ctx, l.base.Client(ctx), params, page)
	})
}

// Creator is the store-tier Base of interfaces.Creator: Create runs its statement as a
// write, only inside a transaction, on the transaction's client.
type Creator[C, T, In any] struct {
	// create is the statement.
	create CreateFunc[C, T, In]

	// base runs the statement.
	base Base[C]
}

// NewCreator builds the Creator running create through base.
func NewCreator[C, T, In any](
	base Base[C],
	create CreateFunc[C, T, In],
) Creator[C, T, In] {
	return Creator[C, T, In]{base: base, create: create}
}

// Create persists a new entity built from in.
func (w Creator[C, T, In]) Create(ctx context.Context, in In) (*T, error) {
	return w.base.Write(ctx, "Create", func(ctx context.Context) (*T, error) {
		return w.create(ctx, w.base.Client(ctx), in)
	})
}

// Updater is the store-tier Base of interfaces.Updater: Update runs its statement as a
// write, only inside a transaction, on the transaction's client.
type Updater[C, T any, ID comparable, In any] struct {
	// update is the statement.
	update UpdateFunc[C, T, ID, In]

	// base runs the statement.
	base Base[C]
}

// NewUpdater builds the Updater running update through base.
func NewUpdater[C, T any, ID comparable, In any](
	base Base[C],
	update UpdateFunc[C, T, ID, In],
) Updater[C, T, ID, In] {
	return Updater[C, T, ID, In]{base: base, update: update}
}

// Update modifies the entity identified by id with in.
func (w Updater[C, T, ID, In]) Update(ctx context.Context, id ID, in In) (*T, error) {
	return w.base.Write(ctx, "Update", func(ctx context.Context) (*T, error) {
		return w.update(ctx, w.base.Client(ctx), id, in)
	})
}

// Deleter is the store-tier Base of interfaces.Deleter: Delete runs its statement as a
// write, only inside a transaction, on the transaction's client.
type Deleter[C any, ID comparable] struct {
	// del is the statement.
	del DeleteFunc[C, ID]

	// base runs the statement.
	base Base[C]
}

// NewDeleter builds the Deleter running del through base.
func NewDeleter[C any, ID comparable](
	base Base[C],
	del DeleteFunc[C, ID],
) Deleter[C, ID] {
	return Deleter[C, ID]{base: base, del: del}
}

// Delete removes the entity identified by id.
func (w Deleter[C, ID]) Delete(ctx context.Context, id ID) error {
	_, err := w.base.Write(ctx, "Delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.del(ctx, w.base.Client(ctx), id)
	})
	return err
}

// Exister is the store-tier Base of interfaces.Exister: Exists runs its statement
// through the tier on the client the context's statement runs on.
type Exister[C any, ID comparable] struct {
	// exists is the statement.
	exists ExistsFunc[C, ID]

	// base runs the statement.
	base Base[C]
}

// NewExister builds the Exister running exists through base.
func NewExister[C any, ID comparable](
	base Base[C],
	exists ExistsFunc[C, ID],
) Exister[C, ID] {
	return Exister[C, ID]{base: base, exists: exists}
}

// Exists reports whether an entity identified by id exists.
func (r Exister[C, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	return r.base.Exec(ctx, "Exists", func(ctx context.Context) (bool, error) {
		return r.exists(ctx, r.base.Client(ctx), id)
	})
}

// Funcs are a Resource's statements, each run on the client its context's statement runs
// on: Get, List and Exists read, and Create (from CIn), Update (from UIn) and Delete
// write.
type Funcs[C, T, P any, ID comparable, CIn, UIn any] struct {
	// Get reads the entity identified by id.
	Get GetFunc[C, T, ID]

	// List reads the page of entities params and page select.
	List ListFunc[C, T, P]

	// Create writes a new entity built from in.
	Create CreateFunc[C, T, CIn]

	// Update writes in to the entity identified by id.
	Update UpdateFunc[C, T, ID, UIn]

	// Delete removes the entity identified by id.
	Delete DeleteFunc[C, ID]

	// Exists reports whether an entity identified by id exists.
	Exists ExistsFunc[C, ID]
}

// Resource is the store-tier Base of interfaces.Resource, composed of the role Bases:
// its reads run through the tier and its writes only inside a transaction.
type Resource[C, T, P any, ID comparable, CIn, UIn any] struct {
	// Reader contributes Get.
	Reader[C, T, ID]
	// Lister contributes List.
	Lister[C, T, P]
	// Creator contributes Create.
	Creator[C, T, CIn]
	// Updater contributes Update.
	Updater[C, T, ID, UIn]
	// Deleter contributes Delete.
	Deleter[C, ID]
	// Exister contributes Exists.
	Exister[C, ID]
}

// NewResource builds the Resource running f's statements through base. A missing
// statement fails INTERNAL, naming its role, so a partly-built resource fails at the
// composition root rather than on its first call.
func NewResource[C, T, P any, ID comparable, CIn, UIn any](
	base Base[C],
	f Funcs[C, T, P, ID, CIn, UIn],
) (Resource[C, T, P, ID, CIn, UIn], error) {
	for _, s := range []struct {
		role    string
		missing bool
	}{
		{"Get", f.Get == nil},
		{"List", f.List == nil},
		{"Create", f.Create == nil},
		{"Update", f.Update == nil},
		{"Delete", f.Delete == nil},
		{"Exists", f.Exists == nil},
	} {
		if s.missing {
			return Resource[C, T, P, ID, CIn, UIn]{}, apperr.New(
				apperr.CodeInternal,
				"store "+base.Name()+": no "+s.role+" statement",
			)
		}
	}
	return Resource[C, T, P, ID, CIn, UIn]{
		Reader:  NewReader(base, f.Get),
		Lister:  NewLister(base, f.List),
		Creator: NewCreator(base, f.Create),
		Updater: NewUpdater(base, f.Update),
		Deleter: NewDeleter(base, f.Delete),
		Exister: NewExister(base, f.Exists),
	}, nil
}
