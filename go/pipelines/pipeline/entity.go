package pipeline

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Entity is the pipeline-tier entity base: a Base that also serves the entity's roles
// over N, the next tier's contract (a service), each as one operation of the pipeline. A
// CRUD pipeline is its Entity and nothing else; a pipeline with product steps adds them
// through Exec or Stage, calling Next.
type Entity[T, P any, ID comparable, N any] struct {
	// Entity serves the roles.
	decorate.Entity[T, P, ID]

	// next is the service.
	next N

	// Base runs the pipeline's operations and supplies Name, Exec, Stage and FanOut.
	Base
}

// Next returns the service the pipeline is built over, as its contract declares it.
func (e Entity[T, P, ID, N]) Next() N { return e.next }

// NewEntity builds the Entity of the pipeline named name over next, whose contract is C
// (an interface), serving the roles C has from next's methods: decorated as NewBase
// decorates. A role C names that next lacks is CodeInternal.
func NewEntity[C, T, P any, ID comparable, N any](
	decs decorate.Decorations,
	name string,
	next N,
) (Entity[T, P, ID, N], error) {
	b, err := NewBase[C](decs, name)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	e, err := decorate.NewEntity[C](name, decorate.RolesOf[T, P, ID](next), b.run, nil)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	return Entity[T, P, ID, N]{Base: b, Entity: e, next: next}, nil
}

// run runs every role call through the pipeline's chain.
func (b Base) run(
	ctx context.Context,
	op string,
	_ decorate.RoleKind,
	fn func(context.Context) error,
) error {
	_, err := b.Exec(
		ctx,
		op,
		func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) },
	)
	return err
}
