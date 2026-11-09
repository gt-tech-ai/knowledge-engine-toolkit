package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Entity is the workflow-tier entity base: a Base that also serves the entity's roles
// over N, the next tier's contract (a pipeline). A read runs as one operation with no
// transaction, and every write (Create, Update, Delete, Insert, Upsert, PurgeBefore) as
// one command in its own transaction. A workflow embeds it and adds its product
// operations through Exec, ExecTx or InTx, calling Next.
type Entity[T, P any, ID comparable, N any] struct {
	// Entity serves the roles.
	decorate.Entity[T, P, ID]

	// Base runs the workflow's operations and supplies Name, Exec, ExecTx and InTx.
	Base

	// next is the pipeline.
	next N
}

// Next returns the pipeline the workflow is built over, as its contract declares it.
func (e Entity[T, P, ID, N]) Next() N { return e.next }

// NewEntity builds the Entity of the workflow named name over next, whose contract is W
// (an interface), serving the roles W has from next's methods and committing each write
// through tx: decorated as NewBase decorates. A role W names that next lacks is
// CodeInternal.
func NewEntity[W, T, P any, ID comparable, N any](
	decs decorate.Decorations,
	name string,
	tx interfaces.TransactionManager,
	next N,
) (Entity[T, P, ID, N], error) {
	base, err := NewBase[W](decs, name, tx)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}

	e, err := decorate.NewEntity[W](name, decorate.RolesOf[T, P, ID](next), base.run, nil)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}

	return Entity[T, P, ID, N]{Base: base, Entity: e, next: next}, nil
}

// run runs a read through Exec and a write through ExecTx.
func (b Base) run(
	ctx context.Context,
	operation string,
	kind decorate.RoleKind,
	fn func(context.Context) error,
) error {
	body := func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }
	if kind == decorate.RoleRead {
		_, err := b.Exec(ctx, operation, body)

		return err
	}

	_, err := b.ExecTx(ctx, operation, body)

	return err
}
