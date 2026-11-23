//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outboxwake"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
)

const (
	// wakeChannel is the channel the waker tests listen on.
	wakeChannel = "outbox_wake"

	// wakeWait bounds a wait for a wake that must arrive.
	wakeWait = 10 * time.Second

	// wakeQuiet is how long a test waits to prove no wake arrives.
	wakeQuiet = 300 * time.Millisecond

	// wakeCloseBudget is the time Close has to return.
	wakeCloseBudget = time.Second

	// wakeBurst is how many notifications overflow the one-slot buffer.
	wakeBurst = 5
)

// startWaker starts Postgres and a pg_notify waker over it on a buffer of buffer,
// listening on wakeChannel, and returns the waker and a database handle for NOTIFY.
func startWaker(t *testing.T, buffer int) (interfaces.OutboxWaker, *sql.DB) {
	t.Helper()

	container, err := postgres.NewTestDatabase(t.Context())
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { container.Close(context.Background()) })

	database, err := container.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	waker, err := outboxwake.NewFromConfig(
		t.Context(),
		&outboxwake.Config{
			Kind:    outboxwake.KindPgNotify,
			Channel: wakeChannel,
			Buffer:  buffer,
		},
		outboxwake.Deps{DSN: container.GetDSN(), Logger: nil, Metrics: nil},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = waker.Close() })
	require.NoError(t, waker.Start(t.Context()))

	return waker, database
}

// notify sends payload on wakeChannel.
func notify(t *testing.T, database *sql.DB, payload string) {
	t.Helper()

	_, err := database.ExecContext(
		t.Context(),
		"SELECT pg_notify($1, $2)",
		wakeChannel,
		payload,
	)
	require.NoError(t, err)
}

// nextWake waits up to wakeWait for the next wake.
func nextWake(t *testing.T, waker interfaces.OutboxWaker) types.OutboxWake {
	t.Helper()

	select {
	case wake := <-waker.Wakes():
		return wake
	case <-time.After(wakeWait):
		require.FailNow(t, "no wake arrived")

		return types.OutboxWake{Partition: 0, All: false}
	}
}

// TestPgNotifyWaker_WakesThePartition tests how a notification becomes a wake.
//
// Why this test is important:
//   - The relay that owns a partition must wake for its rows only, and a payload the
//     waker cannot read must still wake someone: a lost wake costs a poll interval,
//     a swallowed one costs the latency the waker exists to remove.
//
// What it tests:
//   - "<lane>:<n>" wakes partition n; "<lane>" and "<lane>:x" wake every partition.
func TestPgNotifyWaker_WakesThePartition(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	waker, database := startWaker(t, 0)

	notify(t, database, "docs:3")
	require.Equal(t, types.OutboxWake{Partition: 3, All: false}, nextWake(t, waker))

	notify(t, database, "docs")
	require.True(t, nextWake(t, waker).All)

	notify(t, database, "docs:"+strconv.Itoa(-1))
	require.True(t, nextWake(t, waker).All)

	notify(t, database, "docs:x")
	require.True(t, nextWake(t, waker).All)
}

// TestPgNotifyWaker_ReconnectsAndWakesAll tests recovery from a dropped connection.
//
// Why this test is important:
//   - Notifications sent while the listener is disconnected are lost for good. The
//     waker must reconnect on its own and tell the relay to sweep every partition,
//     or rows enqueued during the outage sit until the next poll.
//
// What it tests:
//   - After the server terminates the waker's backend, the waker emits an All wake once
//     it is listening again, and a later notification is delivered normally.
func TestPgNotifyWaker_ReconnectsAndWakesAll(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	waker, database := startWaker(t, 0)

	_, err := database.ExecContext(t.Context(),
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE query LIKE 'LISTEN %' AND pid <> pg_backend_pid()`)
	require.NoError(t, err)

	require.True(t, nextWake(t, waker).All, "reconnect wakes every partition")

	notify(t, database, "docs:1")
	require.Equal(t, types.OutboxWake{Partition: 1, All: false}, nextWake(t, waker))
}

// TestPgNotifyWaker_FullBufferDropsWithoutBlocking tests overflow.
//
// Why this test is important:
//   - A wake is only a hint, so a slow relay must never stall the listener: a blocked
//     listener stops draining the connection and the server's notification queue.
//
// What it tests:
//   - With a one-slot buffer and a burst of notifications nobody reads, the extras are
//     dropped, and once the buffer is drained the waker still delivers a notification
//     sent afterwards.
func TestPgNotifyWaker_FullBufferDropsWithoutBlocking(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	waker, database := startWaker(t, 1)

	for i := range wakeBurst {
		notify(t, database, "docs:"+strconv.Itoa(i))
	}

	first := nextWake(t, waker)
	require.False(t, first.All)
	drainUntilQuiet(waker)

	notify(t, database, "docs:"+strconv.Itoa(wakeBurst))
	require.Equal(t, types.OutboxWake{Partition: wakeBurst, All: false},
		nextWakeAfterBurst(t, waker))
}

// drainUntilQuiet reads wakes until none arrives for wakeQuiet. The listener may still
// be delivering the burst after the first read, so a single read would leave a late
// burst wake in the buffer and the next notification would be dropped behind it.
func drainUntilQuiet(waker interfaces.OutboxWaker) {
	for {
		select {
		case <-waker.Wakes():
		case <-time.After(wakeQuiet):
			return
		}
	}
}

// nextWakeAfterBurst returns the first wake that is not one of the burst's partitions
// (0 to wakeBurst-1), skipping a burst wake that arrived after the drain went quiet.
func nextWakeAfterBurst(t *testing.T, waker interfaces.OutboxWaker) types.OutboxWake {
	t.Helper()

	for {
		if wake := nextWake(t, waker); wake.All || wake.Partition >= wakeBurst {
			return wake
		}
	}
}

// TestPgNotifyWaker_CloseReturnsPromptly tests shutdown.
//
// Why this test is important:
//   - A relay's shutdown waits on its waker; a Close that hangs on a blocked read
//     would hold up the whole process's graceful stop.
//
// What it tests:
//   - Close returns within a second while the listener is blocked waiting, and again
//     on a second call.
func TestPgNotifyWaker_CloseReturnsPromptly(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	waker, _ := startWaker(t, 0)

	start := time.Now()

	require.NoError(t, waker.Close())
	require.Less(t, time.Since(start), wakeCloseBudget)

	require.NoError(t, waker.Close())
}

// TestPgNotifyWaker_StartFailsWhenUnreachable tests the first connect.
//
// Why this test is important:
//   - A relay configured for wake-ups against a database that is down at boot must
//     learn it from Start, not discover a silent waker later.
//
// What it tests:
//   - Start against a closed port returns an error and leaves nothing running: Close
//     afterwards returns nil.
func TestPgNotifyWaker_StartFailsWhenUnreachable(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	waker, err := outboxwake.NewFromConfig(
		t.Context(),
		&outboxwake.Config{
			Kind:    outboxwake.KindPgNotify,
			Channel: wakeChannel,
			Buffer:  0,
		},
		outboxwake.Deps{
			DSN:     "postgres://127.0.0.1:1/db?sslmode=disable&connect_timeout=1",
			Logger:  nil,
			Metrics: nil,
		},
	)
	require.NoError(t, err)

	require.Error(t, waker.Start(t.Context()))
	require.NoError(t, waker.Close())
}
