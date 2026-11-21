package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outboxwake"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

const (
	// wakeNone is how long a test waits to prove the none waker stays silent.
	wakeNone = 50 * time.Millisecond

	// plainChannel is a valid notify channel name.
	plainChannel = "wake"
)

// TestOutboxWaker_DefaultsToNone tests the zero-infrastructure default.
//
// Why this test is important:
//   - A relay must boot and run on its poll with no database connection, so an empty
//     or "none" kind has to yield a waker that needs nothing and never fires.
//
// What it tests:
//   - An empty kind and "none" both build a waker whose Start and Close succeed (Close
//     repeatedly), and whose channel yields nothing.
func TestOutboxWaker_DefaultsToNone(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", string(outboxwake.KindNone)} {
		waker, err := outboxwake.NewFromConfig(
			t.Context(),
			&outboxwake.Config{Kind: client.Kind(kind), Channel: "", Buffer: 0},
			outboxwake.Deps{DSN: "", Logger: nil, Metrics: nil},
		)
		require.NoError(t, err, kind)
		require.NoError(t, waker.Start(t.Context()))

		select {
		case wake := <-waker.Wakes():
			require.FailNow(t, "the none waker woke", "%+v", wake)
		case <-time.After(wakeNone):
		}

		require.NoError(t, waker.Close())
		require.NoError(t, waker.Close())
	}
}

// TestOutboxWaker_RejectsBadConfig tests the factory's validation.
//
// Why this test is important:
//   - A misconfigured waker must fail at boot with a coded error, not silently leave
//     the relay polling: an unknown kind, or a pg_notify waker with nothing to dial or
//     listen on, is a deployment mistake.
//
// What it tests:
//   - An unknown kind, pg_notify with no DSN, with no channel, with a channel that is
//     not a plain identifier, and with a negative buffer are each CodeInvalidInput,
//     and building a valid pg_notify waker does no I/O.
func TestOutboxWaker_RejectsBadConfig(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://127.0.0.1:1/db"

	bad := []struct {
		dsn string
		cfg outboxwake.Config
	}{
		{dsn, outboxwake.Config{Kind: "carrier_pigeon", Channel: "", Buffer: 0}},
		{"", pgNotifyConfig(plainChannel, 0)},
		{dsn, pgNotifyConfig("", 0)},
		{dsn, pgNotifyConfig("Wake; x", 0)},
		{dsn, pgNotifyConfig(plainChannel, -1)},
	}

	for _, row := range bad {
		_, err := outboxwake.NewFromConfig(
			t.Context(),
			&row.cfg,
			outboxwake.Deps{DSN: row.dsn, Logger: nil, Metrics: nil},
		)
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "%+v", row)
	}

	cfg := pgNotifyConfig(plainChannel, 0)
	waker, err := outboxwake.NewFromConfig(
		t.Context(),
		&cfg,
		outboxwake.Deps{DSN: dsn, Logger: nil, Metrics: nil},
	)
	require.NoError(t, err)
	require.NoError(t, waker.Close())
}

// pgNotifyConfig is a pg_notify waker config on channel with a buffer of buffer.
func pgNotifyConfig(channel string, buffer int) outboxwake.Config {
	return outboxwake.Config{
		Kind:    outboxwake.KindPgNotify,
		Channel: channel,
		Buffer:  buffer,
	}
}
