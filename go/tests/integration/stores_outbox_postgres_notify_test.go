//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	outboxpg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/sqltx"
)

const (
	// notifyChannel is the channel the notify tests configure and LISTEN on.
	notifyChannel = "outbox_wake"

	// notifyWait bounds a wait for a notification that must arrive.
	notifyWait = 5 * time.Second

	// notifyQuiet is how long a test waits to prove no notification arrives.
	notifyQuiet = 500 * time.Millisecond

	// notifyPartitions is the partition count of the partitioned notify test.
	notifyPartitions = 4
)

// listenOn opens a connection of its own, LISTENs on channel and returns it; the
// connection is closed when the test ends.
func listenOn(t *testing.T, dsn, channel string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	_, err = conn.Exec(t.Context(), "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)

	return conn
}

// enqueueIn enqueues a record of lane keyed by key inside a transaction that runs
// finish after the insert and ends with finish's result.
func enqueueIn(
	t *testing.T,
	store *outboxpg.Store,
	database *sql.DB,
	lane, key string,
	finish error,
) error {
	t.Helper()

	rec := types.OutboxRecord{
		ID:         uuid.New(),
		Lane:       lane,
		Tenant:     textOrg1,
		Key:        key,
		Payload:    []byte(`{}`),
		Attributes: map[string]string{textEventType: "made.v1"},
		CreatedAt:  time.Time{},
		Attempts:   0,
	}

	return sqltx.New(database).WithTransaction(
		t.Context(),
		func(ctx context.Context) error {
			require.NoError(t, store.Enqueue(ctx, &rec))

			return finish
		},
	)
}

// enqueueCommitted enqueues a record of lane keyed by key in a transaction that
// commits.
func enqueueCommitted(
	t *testing.T,
	store *outboxpg.Store,
	database *sql.DB,
	lane, key string,
) {
	t.Helper()

	require.NoError(t, enqueueIn(t, store, database, lane, key, nil))
}

// enqueueRolledBack enqueues a record of lane keyed by key in a transaction that
// rolls back.
func enqueueRolledBack(
	t *testing.T,
	store *outboxpg.Store,
	database *sql.DB,
	lane, key string,
) {
	t.Helper()

	rollback := apperr.New(apperr.CodeConflict, "roll back")

	require.ErrorIs(t, enqueueIn(t, store, database, lane, key, rollback), rollback)
}

// nextNotification waits up to notifyWait for the next notification on conn and
// returns its payload.
func nextNotification(t *testing.T, conn *pgx.Conn) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), notifyWait)
	defer cancel()

	note, err := conn.WaitForNotification(ctx)
	require.NoError(t, err, "wait for a notification")

	return note.Payload
}

// TestPgstore_NotifiesOnCommitOnly tests the store's wake-up signal.
//
// Why this test is important:
//   - A relay that wakes on a notification must wake only for a row it can claim. A
//     notification sent for a transaction that rolled back wakes the relay for nothing;
//     one sent before the commit lets the relay claim before the row is visible and
//     sleep again, so the signal buys no latency.
//
// What it tests:
//   - A LISTENing connection receives the lane's name after the enqueuing transaction
//     commits.
//   - A rolled-back enqueue sends nothing: the next notification is the later commit's.
func TestPgstore_NotifiesOnCommitOnly(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	store, database, dsn := newPgstoreWith(t, func(row *outboxpg.Config) {
		row.NotifyChannel = notifyChannel
	})
	conn := listenOn(t, dsn, notifyChannel)

	enqueueRolledBack(t, store, database, "rolled-back", uuid.NewString())
	enqueueCommitted(t, store, database, "committed", uuid.NewString())

	require.Equal(t, "committed", nextNotification(t, conn))
}

// TestPgstore_NotifiesThePartition tests that the payload names the row's partition.
//
// Why this test is important:
//   - One relay drains each partition of a lane; the payload is how a wake-up reaches
//     the relay that owns the row instead of waking all of them.
//
// What it tests:
//   - With a partition column the payload is "<lane>:<partition>", the partition being
//     types.OutboxPartition of the key.
func TestPgstore_NotifiesThePartition(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	store, database, dsn := newPgstoreWith(t, func(row *outboxpg.Config) {
		row.NotifyChannel = notifyChannel
		row.PartitionColumn = "partition"
		row.Partitions = notifyPartitions
	})
	conn := listenOn(t, dsn, notifyChannel)
	key := uuid.NewString()

	enqueueCommitted(t, store, database, "docs", key)

	want := "docs:" + strconv.Itoa(types.OutboxPartition(key, notifyPartitions))
	require.Equal(t, want, nextNotification(t, conn))
}

// TestPgstore_WithoutAChannelDoesNotNotify tests the default.
//
// Why this test is important:
//   - A store with no notify channel is the polling-only deployment; it must not emit
//     notifications nobody asked for, nor fail for lack of a listener.
//
// What it tests:
//   - An enqueue with an empty NotifyChannel commits, and a connection LISTENing on the
//     channel receives nothing.
func TestPgstore_WithoutAChannelDoesNotNotify(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	store, database, dsn := newPgstoreWith(t, func(*outboxpg.Config) {})
	conn := listenOn(t, dsn, notifyChannel)

	enqueueCommitted(t, store, database, "quiet", uuid.NewString())

	ctx, cancel := context.WithTimeout(t.Context(), notifyQuiet)
	defer cancel()

	_, err := conn.WaitForNotification(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
