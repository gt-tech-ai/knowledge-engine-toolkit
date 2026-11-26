package connect

import (
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// maxAgeJitterDivisor bounds the jitter spread added to each connection's age to
// maxAge/divisor, so connections opened together do not all close at the same instant and
// stampede the server with reconnections.
const maxAgeJitterDivisor = 10

// NewConnAgeLimiter returns an http.Server ConnState hook that bounds how long a
// connection lives: once it is older than maxAge it is closed the next time it goes idle
// (no in-flight request is cut), and if it never goes idle it is force-closed
// maxAge+grace after it was accepted. Each connection's maxAge carries up to maxAge/10
// of staggering
// jitter so reconnections do not synchronise. The point is to make a client re-resolve
// DNS: a round_robin client dialing a headless Kubernetes Service picks up pods added
// by a scale-up only when a connection ends, and a Connect server runs over net/http,
// which
// cannot send the HTTP/2 GOAWAY a native gRPC server uses (the transport/grpc
// ServerConfig.MaxConnectionAge). A non-positive maxAge returns nil, which leaves
// http.Server.ConnState unset (today's behaviour: a connection lives until the pod dies).
func NewConnAgeLimiter(maxAge, grace time.Duration) func(net.Conn, http.ConnState) {
	if maxAge <= 0 {
		return nil
	}

	limiter := &connAgeLimiter{
		conns:   make(map[net.Conn]*connAgeEntry),
		maxAge:  maxAge,
		grace:   grace,
		mutex:   sync.Mutex{},
		counter: atomic.Int64{},
	}

	return limiter.onState
}

// connAgeEntry tracks one connection's age deadline and its hard force-close timer.
type connAgeEntry struct {
	timer    *time.Timer
	deadline time.Time
}

// connAgeLimiter closes connections once they pass their age deadline, gracefully at the
// next idle or forcibly after the grace window.
type connAgeLimiter struct {
	conns   map[net.Conn]*connAgeEntry
	maxAge  time.Duration
	grace   time.Duration
	mutex   sync.Mutex
	counter atomic.Int64
}

// onState is the http.Server ConnState hook: it starts an age deadline for a new
// connection, closes an aged connection when it next goes idle, and forgets a connection
// that closes.
func (l *connAgeLimiter) onState(conn net.Conn, state http.ConnState) {
	if state == http.StateNew {
		l.track(conn)

		return
	}

	if state == http.StateIdle {
		l.closeIfAged(conn)

		return
	}

	if state == http.StateClosed || state == http.StateHijacked {
		l.forget(conn)
	}
}

// age returns this connection's lifetime: maxAge plus a deterministic per-connection
// stagger in [0, maxAge/10), derived from a monotonic counter so closes spread out
// without a random source.
func (l *connAgeLimiter) age() time.Duration {
	span := l.maxAge / maxAgeJitterDivisor
	if span <= 0 {
		return l.maxAge
	}

	seq := l.counter.Add(1)
	jitter := span * time.Duration(seq%maxAgeJitterDivisor) / maxAgeJitterDivisor

	return l.maxAge + jitter
}

// track records a new connection's age deadline and schedules its hard force-close.
func (l *connAgeLimiter) track(conn net.Conn) {
	age := l.age()

	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.conns[conn] = &connAgeEntry{
		timer: time.AfterFunc(age+l.grace, func() {
			l.forget(conn)

			_ = conn.Close()
		}),
		deadline: time.Now().Add(age),
	}
}

// closeIfAged closes a connection that has passed its age deadline, now that it is idle.
func (l *connAgeLimiter) closeIfAged(conn net.Conn) {
	l.mutex.Lock()

	entry, tracked := l.conns[conn]
	aged := tracked && !time.Now().Before(entry.deadline)

	if aged {
		entry.timer.Stop()
		delete(l.conns, conn)
	}

	l.mutex.Unlock()

	if aged {
		_ = conn.Close()
	}
}

// forget stops a connection's force-close timer and drops it from tracking.
func (l *connAgeLimiter) forget(conn net.Conn) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	entry, tracked := l.conns[conn]
	if tracked {
		entry.timer.Stop()
		delete(l.conns, conn)
	}
}
