// Package errors maps a store's database errors to domain errors: each mapper translates
// driver-specific errors (constraint violations, not-found, schema errors) into a coded
// core/errors.AppError. The mapper is chosen by the store's database Kind, one
// subpackage per kind (postgres/: pgx and pgconn).
//
// Only the generic, dependency-light mappers live here. An Ent-ORM mapper belongs with
// the consumer that owns the Ent schema, because it imports the schema-generated ent
// error predicates; that consumer's Ent store injects it as a DBMapperFunc — the seam
// type below — at its composition point.
package errors

import (
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/errors/postgres"
)

// DBMapperFunc maps a database error to an *AppError.
// Implementations must return nil when the input error is nil.
// A concrete ORM mapper (e.g. a consumer's Ent mapper) is a DBMapperFunc value
// injected by the store that owns that ORM.
type DBMapperFunc func(error) error

// KindPostgres maps PostgreSQL errors (pgx, pgconn) to domain errors (the default).
const KindPostgres = "postgres"

// NewDBMapper returns the DBMapperFunc for the database kind; empty means KindPostgres.
// An unknown kind is CodeInvalidInput.
func NewDBMapper(kind string) (DBMapperFunc, error) {
	switch kind {
	case "", KindPostgres:
		return postgres.MapDBError, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"db mapper: unknown kind \""+kind+"\" (want postgres)",
		)
	}
}
