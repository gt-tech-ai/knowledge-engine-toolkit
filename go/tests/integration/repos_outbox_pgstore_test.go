//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/outbox/pgstore"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/outboxtest"
)

// pgstoreSchema is a product-shaped outbox table: the fixed layout plus a tenant, an
// aggregate key and an event_type attribute column.
const pgstoreSchema = `
CREATE TABLE product_outbox (
	id              uuid        PRIMARY KEY,
	lane            text        NOT NULL,
	event_type      text        NOT NULL,
	aggregate_id    uuid        NOT NULL,
	tenant          text        NOT NULL DEFAULT '',
	payload         bytea       NOT NULL,
	status          text        NOT NULL DEFAULT 'pending',
	attempts        integer     NOT NULL DEFAULT 0,
	next_attempt_at timestamptz NOT NULL DEFAULT now(),
	last_error      text        NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now(),
	sent_at         timestamptz
)`

// newPgstore starts Postgres, creates the product table and returns the store over it
// with the producer-side insert the conformance suite drives.
func newPgstore(t *testing.T) (*pgstore.Store, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { pg.Close(context.Background()) })
	db, err := pg.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, pgstoreSchema)
	require.NoError(t, err)
	store, err := pgstore.New(db, pgstore.Config{
		Table:            "product_outbox",
		TenantColumn:     "tenant",
		AttributeColumns: map[string]string{"event_type": "event_type"},
		Lease:            time.Minute,
		MaxErrorLen:      16,
	})
	require.NoError(t, err)
	return store, db
}

// pgstoreEnqueue inserts rec as a due pending row, with the record's Key as the
// aggregate id and its event_type attribute in the column.
func pgstoreEnqueue(db *sql.DB) func(context.Context, *types.OutboxRecord) error {
	return func(ctx context.Context, rec *types.OutboxRecord) error {
		key, err := uuid.Parse(rec.Key)
		if err != nil {
			key = uuid.NewSHA1(uuid.NameSpaceOID, []byte(rec.Key))
		}
		created := rec.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		_, err = db.ExecContext(ctx, `INSERT INTO product_outbox
			(id, lane, event_type, aggregate_id, tenant, payload, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			rec.ID, rec.Lane, rec.Attributes["event_type"], key, rec.Tenant, rec.Payload,
			created)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "pgstore test: enqueue")
		}
		return nil
	}
}

// TestPgstore_PassesTheOutboxConformanceSuite tests the production store against the
// relay's contract.
//
// Why this test is important:
//   - Every product outbox drains through this store; a claim that overlapped, ignored
//     next_attempt_at, or re-delivered a sent row breaks at-least-once delivery.
//
// What it tests:
//   - The shared OutboxStore conformance suite passes over a product-shaped table.
func TestPgstore_PassesTheOutboxConformanceSuite(t *testing.T) {
	requireIntegration(t)
	store, db := newPgstore(t)
	outboxtest.Run(t, outboxtest.Harness{Store: store, Enqueue: pgstoreEnqueue(db)})
}

// TestPgstore_MapsColumnsAndPurgesSentRows tests the configured columns and retention.
//
// Why this test is important:
//   - A sink keys its output by the record's tenant, key and attributes; a wrong column
//     would route every event to the wrong place. Sent rows that are never purged grow
//     the table without bound.
//
// What it tests:
//   - A claimed record carries the tenant, the aggregate id as Key and the event_type
//     attribute; a long last error is truncated to MaxErrorLen.
//   - DeleteSentBefore removes only sent rows older than the cutoff.
func TestPgstore_MapsColumnsAndPurgesSentRows(t *testing.T) {
	requireIntegration(t)
	ctx := t.Context()
	store, db := newPgstore(t)
	key := uuid.New()
	sent, parked := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{sent, parked} {
		require.NoError(t, pgstoreEnqueue(db)(ctx, &types.OutboxRecord{
			ID: id, Lane: "facts", Tenant: "org-1", Key: key.String(),
			Payload: []byte(`{}`), Attributes: map[string]string{"event_type": "fact.v1"},
		}))
	}

	recs, err := store.Claim(ctx, "facts", 10)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	require.Equal(t, "org-1", recs[0].Tenant)
	require.Equal(t, key.String(), recs[0].Key)
	require.Equal(t, map[string]string{"event_type": "fact.v1"}, recs[0].Attributes)

	require.NoError(t, store.MarkSent(ctx, sent))
	require.NoError(t, store.Park(ctx, parked, strings.Repeat("x", 40)))
	var lastErr string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT last_error FROM product_outbox WHERE id = $1`, parked).Scan(&lastErr))
	require.Len(t, lastErr, 16)

	n, err := store.DeleteSentBefore(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, n, "the sent row is newer than the cutoff")
	n, err = store.DeleteSentBefore(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the sent row is purged; the parked row stays")
}
