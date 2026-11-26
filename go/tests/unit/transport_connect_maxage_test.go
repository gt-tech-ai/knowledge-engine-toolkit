package unit_test

import (
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect"
)

const (
	// connAgeShort is the short connection age the limiter tests run with.
	connAgeShort = 50 * time.Millisecond

	// connAgeDeadline bounds how long a test waits for a force-close.
	connAgeDeadline = 2 * time.Second
)

// countingConn is a net.Conn spy that records whether Close was called, over a real
// net.Pipe connection so every other net.Conn method behaves normally. It is the
// external-boundary double for the connection the limiter closes.
type countingConn struct {
	net.Conn

	closed atomic.Bool
}

// Close records the close and closes the underlying pipe.
func (c *countingConn) Close() error {
	c.closed.Store(true)

	_ = c.Conn.Close()

	return nil
}

// newCountingConn returns a countingConn over one end of a fresh net.Pipe, closing both
// ends on test cleanup.
func newCountingConn(t *testing.T) *countingConn {
	t.Helper()

	client, server := net.Pipe()

	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	return &countingConn{Conn: server, closed: atomic.Bool{}}
}

// TestNewConnAgeLimiter_ClosesAgedConnectionOnIdle tests graceful age-based closing.
//
// Why this test is important:
//   - The limiter makes a client reconnect and re-resolve DNS so it sees scaled-up pods,
//     but it must not cut a request mid-flight: a connection is closed only once it is
//     older than maxAge AND idle.
//
// What it tests:
//   - An idle connection younger than maxAge is left open; once older than maxAge, the
//     next idle transition closes it.
func TestNewConnAgeLimiter_ClosesAgedConnectionOnIdle(t *testing.T) {
	t.Parallel()

	onState := connect.NewConnAgeLimiter(connAgeShort, time.Hour)
	require.NotNil(t, onState)

	conn := newCountingConn(t)

	onState(conn, http.StateNew)
	onState(conn, http.StateIdle)
	assert.False(t, conn.closed.Load(), "young connection stays open at idle")

	time.Sleep(connAgeShort + connAgeShort/2)

	onState(conn, http.StateIdle)
	assert.True(t, conn.closed.Load(), "aged connection is closed at idle")
}

// TestNewConnAgeLimiter_ForceClosesConnectionPastGrace tests the hard force-close.
//
// Why this test is important:
//   - A connection that never goes idle (a long-lived stream) would otherwise live
//     forever and never let its client re-resolve; the force-close bounds that.
//
// What it tests:
//   - A connection left in StateNew is closed on its own once maxAge+grace elapses, with
//     no idle transition.
func TestNewConnAgeLimiter_ForceClosesConnectionPastGrace(t *testing.T) {
	t.Parallel()

	onState := connect.NewConnAgeLimiter(connAgeShort, connAgeShort)
	require.NotNil(t, onState)

	conn := newCountingConn(t)

	onState(conn, http.StateNew)

	require.Eventually(t, conn.closed.Load, connAgeDeadline, connAgeShort/5,
		"connection that never idles is force-closed past maxAge+grace")
}

// TestNewConnAgeLimiter_DisabledReturnsNil tests the off switch.
//
// Why this test is important:
//   - A non-positive maxAge must leave http.Server.ConnState unset so the server keeps
//     its default behaviour (a connection lives until the pod dies).
//
// What it tests:
//   - NewConnAgeLimiter with a non-positive maxAge returns a nil hook.
func TestNewConnAgeLimiter_DisabledReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, connect.NewConnAgeLimiter(0, time.Second))
}
