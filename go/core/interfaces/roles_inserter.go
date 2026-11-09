package interfaces

import (
	"context"
	"time"
)

// Inserter is the append-only write role: a single Insert that persists a new
// entity and reports only success/failure. Unlike Writer (Create returns the
// stored entity + Update mutates), an append-only store — an outbox relay, an
// audit log — only ever adds immutable rows, so it embeds just this. It is NOT
// part of the Store/Repository/Service composition (those use Writer.Create);
// it is the standalone role an append-only resource embeds in place of Writer.
type Inserter[T any] interface {
	// Insert persists a new entity; append-only, so it returns only an error.
	Insert(ctx context.Context, entity *T) error
}

// Upserter writes an entity, inserting it or replacing the one with the same key, and
// reports only success or failure. It runs inside the caller's transaction like Insert.
type Upserter[T any] interface {
	// Upsert inserts entity or replaces the stored one with its key.
	Upsert(ctx context.Context, entity *T) error
}

// Purger removes the entities older than a cutoff: a retention sweep.
type Purger interface {
	// PurgeBefore removes the entities older than cutoff and returns how many it removed.
	PurgeBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// Deleter removes an entity by its identifier.
type Deleter[ID comparable] interface {
	// Delete removes an entity by ID.
	Delete(ctx context.Context, id ID) error
}

// Exister reports whether an entity with the given identifier exists.
type Exister[ID comparable] interface {
	// Exists checks if an entity with the given ID exists.
	Exists(ctx context.Context, id ID) (bool, error)
}
