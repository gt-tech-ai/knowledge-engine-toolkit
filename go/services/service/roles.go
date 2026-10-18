package service

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// CRUD is the decorated service over repo: every operation runs through the service's
// decoration, and a non-nil validate rejects an invalid entity before Create or Update.
func (b Base) CRUD[T, P any, ID comparable](
	repo interfaces.Repository[T, P, ID],
	validate func(*T) error,
) interfaces.Service[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
	}{
		decorate.NewReader(b.Subject, repo),
		decorate.NewLister(b.Subject, repo),
		decorate.NewWriter(b.Subject, b.Subject, repo, validate),
		decorate.NewDeleter(b.Subject, repo),
	}
}

// Validators check a Resource's write inputs before they reach the repository; a nil
// validator accepts every input.
type Validators[C, U any] struct {
	// Create checks a Create input.
	Create func(C) error

	// Update checks an Update input.
	Update func(U) error
}

// Resource is the decorated service over repo, a resource whose Create takes C and
// whose Update takes U: every role runs through the service's decoration, and v rejects
// an invalid write input before it reaches repo.
func (b Base) Resource[T, P any, ID comparable, C, U any](
	repo interfaces.Resource[T, P, ID, C, U],
	v Validators[C, U],
) interfaces.Resource[T, P, ID, C, U] {
	return decorate.NewResource[T, P, ID, C, U](
		decorate.ResourceOps{Create: b.Subject, Rest: b.Subject},
		repo,
		v.Create,
		v.Update,
	)
}

// CRUDNoList is CRUD for a resource with no listable collection.
func (b Base) CRUDNoList[T any, ID comparable](
	repo interfaces.CRUDNoListRepository[T, ID],
	validate func(*T) error,
) interfaces.CRUDNoListRepository[T, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Writer[T, ID]
		decorate.Deleter[ID]
	}{
		decorate.NewReader(b.Subject, repo),
		decorate.NewWriter(b.Subject, b.Subject, repo, validate),
		decorate.NewDeleter(b.Subject, repo),
	}
}

// ReadListExist is the decorated read-only service over repo, for a resource whose
// writes are owned elsewhere.
func (b Base) ReadListExist[T, P any, ID comparable](
	repo interfaces.ReadListExistStore[T, P, ID],
) interfaces.ReadListExistStore[T, P, ID] {
	return struct {
		decorate.Reader[T, ID]
		decorate.Lister[T, P]
		decorate.Exister[ID]
	}{
		decorate.NewReader(b.Subject, repo),
		decorate.NewLister(b.Subject, repo),
		decorate.NewExister(b.Subject, repo),
	}
}
