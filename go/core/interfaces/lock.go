package interfaces

import "context"

// DistributedLock is a keyed, token-fenced, best-effort mutual-exclusion
// primitive: it lets one holder claim a string key across processes, so that
// "at most one active operation per key" holds across service replicas
// (high-cardinality, short-held, best-effort; a Postgres advisory lock suits a
// long-running single writer instead).
//
// It is the seam a single-active-operation guard depends on;
// callers depend on this interface, never a concrete backend. Backends:
// clients/lock/local (in-process, dev/single-replica) and clients/lock/redis
// (cross-pod, staging/prod), selected by config via clients/lock.NewFromConfig.
//
// Best-effort, NOT true mutual exclusion: a rare double-acquire is possible if a
// holder stalls past the lease TTL, or on a Redis primary→replica failover that
// loses an unacknowledged write (the Redlock critique). Callers must tolerate a
// rare duplicate — the Locker.Hold helper mitigates the stall case by
// cancelling the in-flight work the instant a renewal reports the lease lost.
type DistributedLock interface {
	// Acquire attempts to take key without blocking. On success it returns an
	// opaque fencing token and acquired=true; if the key is already held it
	// returns an empty token and acquired=false. The token must be presented to
	// Renew and Release so a previous holder cannot affect the current one.
	Acquire(ctx context.Context, key string) (token string, acquired bool, err error)

	// Renew extends the lease on key iff token still holds it. held=true means
	// the caller still owns the lock; held=false means the lease was lost (it
	// expired, was released, or a different holder now owns the key) and the
	// caller must stop treating itself as the holder. err is reserved for
	// backend/transport failures, distinct from a cleanly lost lease.
	Renew(ctx context.Context, key, token string) (held bool, err error)

	// Release frees key iff token still holds it (token-fenced). Releasing a key
	// held by a different token, or an unheld key, is a no-op — never an error —
	// so a late Release from a previous holder cannot free a new holder's lock.
	Release(ctx context.Context, key, token string) error
}

// Locker is the consumer-facing lock seam: a DistributedLock plus Hold, the
// managed acquire-with-renewal-watchdog helper. clients/lock.NewFromConfig
// returns a Locker, and a single-active-operation guard depends on this interface.
//
// Hold is declared here rather than on DistributedLock so the primitive backend
// contract stays minimal (interface-segregation): each backend implements only
// Acquire/Renew/Release, while Hold's backend-agnostic orchestration (the renewal
// watchdog and lease-lost cancellation) is provided once by the wrapper that
// composes it over any DistributedLock.
type Locker interface {
	// DistributedLock provides the primitive Acquire/Renew/Release operations.
	DistributedLock

	// Hold acquires key and, on success, keeps its lease alive with a background
	// watchdog that renews on the configured interval. It returns:
	//   - held: a child of ctx that is cancelled the instant the lease is lost (a
	//     Renew reports held=false or errors) or when release is called; run the
	//     guarded work under held so it stops the moment the lock is no longer ours.
	//   - release: stops the watchdog, cancels held, and frees the lock
	//     (token-fenced); idempotent, safe to defer, and frees the key even if ctx
	//     is already cancelled.
	//   - acquired: false if the key was already held by someone else (held is then
	//     a cancelled context and release a no-op — the caller should reject the
	//     request); err is returned only for a backend failure on Acquire.
	Hold(
		ctx context.Context,
		key string,
	) (held context.Context, release func(), acquired bool, err error)
}
