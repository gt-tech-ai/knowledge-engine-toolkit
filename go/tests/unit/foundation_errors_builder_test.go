// Package foundation_test verifies foundation-layer infrastructure helpers:
// DB error mappers, cache decorators, options application, and middleware
// chains. All external I/O is mocked; no real databases or networks are used.
package unit_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/errors"
)

// TestErrorsBuilder_KindPostgres tests that the DB error mapper factory
// produces a working mapper for PostgreSQL.
//
// Why this test is important:
//   - Every repository layer depends on the mapper to translate raw database
//     errors into domain-meaningful application errors; a broken factory means
//     no service can properly handle database failures
//
// What it tests:
//   - NewDBMapper(KindPostgres) returns a non-nil mapper with no error
//   - The mapper converts pgx.ErrNoRows to a non-nil application error
func TestErrorsBuilder_KindPostgres(t *testing.T) {
	t.Parallel()

	mapper, err := errors.NewDBMapper(errors.KindPostgres)
	require.NoError(t, err, "expected nil error from NewDBMapper(KindPostgres)")
	require.NotNil(t, mapper, "expected non-nil mapper")

	// pgx.ErrNoRows maps to a non-nil error (CodeNotFound)
	result := mapper(pgx.ErrNoRows)
	assert.Error(t, result, "expected non-nil error for pgx.ErrNoRows")
}

// TestErrorsBuilder_NilError tests that the DB error mapper is a no-op for nil
// errors.
//
// Why this test is important:
//   - Repository methods pass all errors through the mapper, including nil on success; a
//     mapper that wraps nil into an error would turn every success into a failure
//
// What it tests:
//   - mapper(nil) returns nil
func TestErrorsBuilder_NilError(t *testing.T) {
	t.Parallel()

	mapper, _ := errors.NewDBMapper(errors.KindPostgres)
	result := mapper(nil)
	assert.NoError(t, result, "mapper must return nil for nil input")
}

// TestErrorsBuilder_UnknownKindReturnsError tests that the factory rejects
// unsupported database kinds.
//
// Why this test is important:
//   - The factory must fail loudly at startup when misconfigured; silently
//     producing a nil mapper would cause a panic on the first repository error
//
// What it tests:
//   - NewDBMapper(Kind(999)) returns a non-nil error
func TestErrorsBuilder_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := errors.NewDBMapper(errors.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}
