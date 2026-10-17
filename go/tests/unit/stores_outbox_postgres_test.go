package unit_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	outboxpg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
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
	ok := outboxpg.Config{
		Table:        "aiops_outbox",
		TenantColumn: "tenant",
		Lease:        time.Minute,
	}
	require.NoError(t, ok.Validate())
	bad := []outboxpg.Config{
		{Table: "x; DROP TABLE y", Lease: time.Minute},
		{Table: "t", KeyColumn: `"k"`, Lease: time.Minute},
		{Table: "t", TenantColumn: "Tenant", Lease: time.Minute},
		{Table: "t", AttributeColumns: map[string]string{"a": "1a"}, Lease: time.Minute},
		{Table: "t"},
		{Table: "t", Lease: time.Minute, MaxErrorLen: -1},
	}
	// sql.Open is lazy: a real handle that never connects, since New does no I/O.
	db, err := sql.Open("pgx", "postgres://unused")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, cfg := range bad {
		_, err := outboxpg.New(db, cfg)
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "%+v", cfg)
	}
}

// TestPgstore_RefusesWritesOutsideATransaction tests the store's write guard.
//
// Why this test is important:
//   - A write run on the bare database autocommits outside the workflow that owns
//     it, so a relay that lost its transaction would silently stop being atomic.
//
// What it tests:
//   - Claim, MarkSent, Retry, Park and DeleteSentBefore on a context with no bound
//     transaction each fail INTERNAL before touching the (unreachable) database.
func TestPgstore_RefusesWritesOutsideATransaction(t *testing.T) {
	t.Parallel()
	// sql.Open is lazy: a handle that never connects, so any I/O would fail UNAVAILABLE.
	db, err := sql.Open("pgx", "postgres://unused")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store, err := outboxpg.New(db, outboxpg.Config{Table: "t", Lease: time.Minute})
	require.NoError(t, err)
	ctx, id := t.Context(), uuid.New()

	_, err = store.Claim(ctx, "lane", 10)
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.Equal(t, apperr.CodeInternal, apperr.Code(store.MarkSent(ctx, id)))
	require.Equal(
		t,
		apperr.CodeInternal,
		apperr.Code(store.Retry(ctx, id, time.Now(), "e")),
	)
	require.Equal(t, apperr.CodeInternal, apperr.Code(store.Park(ctx, id, "e")))
	_, err = store.DeleteSentBefore(ctx, time.Now())
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
}
