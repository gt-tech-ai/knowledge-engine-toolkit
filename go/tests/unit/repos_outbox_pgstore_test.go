package unit_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/outbox/pgstore"
)

// TestPgstoreConfig_RejectsUnsafeNamesAndLeases tests config validation.
//
// Why this test is important:
//   - The table and column names are interpolated into SQL; anything but a plain
//     identifier would be an injection point, and a zero lease re-claims rows at once.
//
// What it tests:
//   - A quoted or punctuated table, key, tenant or attribute column, a non-positive
//     lease, and a negative error cap are CodeInvalidInput; a plain config is valid.
func TestPgstoreConfig_RejectsUnsafeNamesAndLeases(t *testing.T) {
	t.Parallel()
	ok := pgstore.Config{
		Table:        "aiops_outbox",
		TenantColumn: "tenant",
		Lease:        time.Minute,
	}
	require.NoError(t, ok.Validate())
	bad := []pgstore.Config{
		{Table: "x; DROP TABLE y", Lease: time.Minute},
		{Table: "t", KeyColumn: `"k"`, Lease: time.Minute},
		{Table: "t", TenantColumn: "Tenant", Lease: time.Minute},
		{Table: "t", AttributeColumns: map[string]string{"a": "1a"}, Lease: time.Minute},
		{Table: "t"},
		{Table: "t", Lease: time.Minute, MaxErrorLen: -1},
	}
	for _, cfg := range bad {
		_, err := pgstore.New(&sql.DB{}, cfg)
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "%+v", cfg)
	}
}
