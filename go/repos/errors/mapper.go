// Package errors provides database error mapping to domain errors.
// Each mapper translates driver-specific errors (constraint violations,
// not-found, schema errors) into core/errors.AppError with appropriate codes.
//
// Only the generic, dependency-light mappers live here (Postgres via pgx/pgconn).
// An Ent-ORM mapper belongs with the consumer that owns the Ent schema, because it
// imports the schema-generated ent error predicates; that consumer's Ent store injects it
// as a DBMapperFunc — the seam type below — at its composition point.
package errors

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/errors/postgres"
)

// DBMapperFunc maps a database error to an *AppError.
// Implementations must return nil when the input error is nil.
// A concrete ORM mapper (e.g. a consumer's Ent mapper) is a DBMapperFunc value
// injected by the store that owns that ORM.
type DBMapperFunc func(error) error

// Kind specifies which built-in DB error mapper to use.
type Kind int

const (
	// KindPostgres maps PostgreSQL-specific errors (pgx, pgconn) to domain errors.
	KindPostgres Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindPostgres:
		return "postgres"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// NewDBMapper creates a DBMapperFunc for the specified kind.
// Returns an error if the kind is unknown.
func NewDBMapper(kind Kind) (DBMapperFunc, error) {
	switch kind {
	case KindPostgres:
		return postgres.MapDBError, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown db mapper kind: %v", kind),
		)
	}
}
