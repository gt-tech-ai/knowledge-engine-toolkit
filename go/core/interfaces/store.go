package interfaces

// Store defines the low-level data access interface that concrete database
// adapters implement. It has the same CRUD surface as Repository but represents
// the infrastructure boundary (e.g., Ent, sqlc, pgx) rather than the
// business-logic boundary. It is composed from the role interfaces,
// so a full-CRUD adapter embeds the whole set while a read-only or append-only
// adapter can embed only the roles it honors.
//
// T = entity type, P = query/filter params, ID = identifier type.
type Store[T any, P any, ID comparable] interface {
	// Reader provides Get (retrieve by id).
	Reader[T, ID]
	// Lister provides List (paginated query by params).
	Lister[T, P]
	// Writer provides Create and Update.
	Writer[T, ID]
	// Deleter provides Delete (remove by id).
	Deleter[ID]
	// Exister provides Exists (existence check by id).
	Exister[ID]
}
