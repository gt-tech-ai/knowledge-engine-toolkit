// Package service is the service tier: a Base is the embeddable core of a consumer's
// service, running its custom operations through the tier's decoration (Tier) with the
// promoted Exec, and building the decorated CRUD services and single-operation
// services the tier offers. Every service it builds is a core interface.
package service

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the service tier's decoration: recovery → tracing → metrics → logging →
// authorization → timeout, outermost first.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "service", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerAuth,
		decorate.LayerTimeout,
	}}
}

// Base is the embeddable core of a service named by its Subject: Name and Exec run a
// custom operation through the tier's decoration. The zero Base runs operations
// undecorated.
type Base struct {
	decorate.Subject
}

// NewBase builds the Base for the service named name, decorated with deps.
func NewBase(name string, deps decorate.Deps) Base {
	return Base{Subject: Tier().Subject(name, deps)}
}

// OpService is the decorated single-operation service whose Run runs run as the
// "Run" operation.
func (b Base) OpService[A, R any](
	run func(ctx context.Context, args A) (R, error),
) interfaces.OpService[A, R] {
	return opService[A, R]{Base: b, run: run}
}

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

// opService is a single-operation service.
type opService[A, R any] struct {
	// run is the operation.
	run func(ctx context.Context, args A) (R, error)

	Base
}

// Run runs the operation through the service's decoration.
func (s opService[A, R]) Run(ctx context.Context, args A) (R, error) {
	return s.Exec(
		ctx,
		"Run",
		func(ctx context.Context) (R, error) { return s.run(ctx, args) },
	)
}
