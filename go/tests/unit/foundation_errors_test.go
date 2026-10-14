package unit_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	repoerrors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/errors"
)

// TestErrorClassification tests that errors are correctly classified as
// transient or permanent with proper HTTP code mapping.
//
// Why this test is important:
//   - Retry logic depends on correct transient/permanent classification; HTTP
//     status mapping ensures API consumers receive semantically correct responses
//
// What it tests:
//   - Timeout is transient, NotFound is permanent
//   - CodeNotFound maps to 404, CodeUnauthorized maps to 401
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	timeoutErr := apperr.Timeout("operation timed out")
	notFoundErr := apperr.NotFound("record not found")

	assert.True(
		t,
		apperr.IsTransient(timeoutErr),
		"timeout error should be transient",
	)
	assert.True(
		t,
		apperr.IsPermanent(notFoundErr),
		"not-found error should be permanent",
	)
	assert.Equal(t, 404, apperr.ToHTTPStatus(apperr.CodeNotFound))
	assert.Equal(t, 401, apperr.ToHTTPStatus(apperr.CodeUnauthorized))
}

// TestDBErrorMapping tests that PostgreSQL error codes are correctly mapped to
// application error codes.
//
// Why this test is important:
//   - Raw PostgreSQL codes leaked to clients expose implementation details and
//     break retry behavior; the mapper must produce consistent AppError codes
//
// What it tests:
//   - nil maps to nil
//   - pgx.ErrNoRows maps to CodeNotFound
//   - SQLSTATE 23505 (unique violation) maps to CodeConflict
//   - SQLSTATE 23503 (FK violation) maps to CodeInvalidInput
//   - SQLSTATE 23502 (not null) maps to CodeInvalidInput
//   - SQLSTATE 42P01 (undefined table) maps to CodeInternal
func TestDBErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err          error
		name         string
		expectedCode apperr.ErrorCode
		expectedNil  bool
	}{
		{
			name:        "nil error",
			err:         nil,
			expectedNil: true,
		},
		{
			name:         "pgx.ErrNoRows",
			err:          pgx.ErrNoRows,
			expectedCode: apperr.CodeNotFound,
		},
		{
			name: "unique violation",
			err: &pgconn.PgError{
				Code:    "23505",
				Message: "duplicate key value",
			},
			expectedCode: apperr.CodeConflict,
		},
		{
			name: "foreign key violation",
			err: &pgconn.PgError{
				Code:    "23503",
				Message: "foreign key constraint",
			},
			expectedCode: apperr.CodeInvalidInput,
		},
		{
			name: "not null violation",
			err: &pgconn.PgError{
				Code:    "23502",
				Message: "null value in column",
			},
			expectedCode: apperr.CodeInvalidInput,
		},
		{
			name: "undefined table",
			err: &pgconn.PgError{
				Code:    "42P01",
				Message: "relation does not exist",
			},
			expectedCode: apperr.CodeInternal,
		},
	}

	mapper, mapperErr := repoerrors.NewDBMapper(repoerrors.KindPostgres)
	require.NoError(t, mapperErr, "mapper factory must not return error")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mappedErr := mapper(tt.err)

			if tt.expectedNil {
				assert.NoError(t, mappedErr, "mapper must return nil for nil input")
				return
			}

			require.Error(t, mappedErr, "mapper must return non-nil error")

			var appErr *apperr.AppError
			require.ErrorAs(
				t,
				mappedErr, &appErr,
				"mapped error must be *AppError, got %T",
				mappedErr,
			)
			assert.Equal(t, tt.expectedCode, appErr.Code)
		})
	}
}
