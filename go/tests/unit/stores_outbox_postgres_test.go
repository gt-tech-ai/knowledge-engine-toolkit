package unit_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	outboxpg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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

	ok := fixtures.Build(func(row *outboxpg.Config) {
		row.Table = "aiops_outbox"
		row.TenantColumn = textTenant
		row.Lease = time.Minute
	})
	require.NoError(t, ok.Validate())

	bad := []outboxpg.Config{
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = "x; DROP TABLE y"
			row.Lease = time.Minute
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.KeyColumn = `"k"`
			row.Lease = time.Minute
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.TenantColumn = "Tenant"
			row.Lease = time.Minute
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.AttributeColumns = map[string]string{textA: "1a"}
			row.Lease = time.Minute
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.Lease = time.Minute
			row.MaxErrorLen = -1
		}),
	}
	// sql.Open is lazy: a real handle that never connects, since New does no I/O.
	db, err := sql.Open(textPgx, textPostgresUnused)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, cfg := range bad {
		_, err := outboxpg.New(db, &cfg)
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
	db, err := sql.Open(textPgx, textPostgresUnused)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store, err := outboxpg.New(
		db,
		fixtures.BuildRef(func(row *outboxpg.Config) {
			row.Table = textT
			row.Lease = time.Minute
		}),
	)
	require.NoError(t, err)

	ctx, identifier := t.Context(), uuid.New()

	_, err = store.Claim(ctx, textLane, 0, claimInput)
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.Equal(t, apperr.CodeInternal, apperr.Code(store.MarkSent(ctx, identifier)))
	require.Equal(
		t,
		apperr.CodeInternal,
		apperr.Code(store.Retry(ctx, identifier, time.Now(), textE)),
	)
	require.Equal(t, apperr.CodeInternal, apperr.Code(store.Park(ctx, identifier, textE)))

	_, err = store.DeleteSentBefore(ctx, time.Now())
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestPgstore_ValidatesPartitions tests the Postgres store's partition settings.
//
// Why this test is important:
//   - Partitions without a column would claim every row from every relay, and a
//     claim for a partition the store does not have would find nothing forever
//
// What it tests:
//   - more than one partition without partition_column, a bad column name and
//     negative partitions are CodeInvalidInput at New; with three partitions,
//     Claim for partition 3 or -1 is CodeInvalidInput before any database call
func TestPgstore_ValidatesPartitions(t *testing.T) {
	t.Parallel()

	value, err := sql.Open(textPgx, textPostgresUnused)
	require.NoError(t, err)
	t.Cleanup(func() { _ = value.Close() })

	base := fixtures.Build(func(row *outboxpg.Config) {
		row.Table = textT
		row.Lease = time.Minute
	})
	for _, cfg := range []outboxpg.Config{
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.Lease = time.Minute
			row.Partitions = partitions
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.Lease = time.Minute
			row.PartitionColumn = "Part"
			row.Partitions = partitions
		}),
		fixtures.Build(func(row *outboxpg.Config) {
			row.Table = textT
			row.Lease = time.Minute
			row.PartitionColumn = "part"
			row.Partitions = -1
		}),
	} {
		_, opErr := outboxpg.New(value, &cfg)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(opErr), "%+v", cfg)
	}

	base.PartitionColumn, base.Partitions = "part", relayPartitionCount

	store, err := outboxpg.New(value, &base)
	require.NoError(t, err)

	ctx := interfaces.WithInTx(context.Background())
	for _, partition := range []int{iterationsPartitions, -1} {
		_, err := store.Claim(ctx, textLane, partition, claimInput)
		assert.Equal(
			t,
			apperr.CodeInvalidInput,
			apperr.Code(err),
			"partition %d",
			partition,
		)
	}
}

const (
	// partitions is the value assigned to row.Partitions in
	// TestPgstore_ValidatesPartitions.
	partitions = 2
)
