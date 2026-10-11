// Package lock builds a config-selected core/interfaces.Locker — a
// DistributedLock (keyed, token-fenced, best-effort) plus Hold, the managed
// acquire-with-watchdog helper a single-active-operation guard depends on.
//
// NewFromConfig chooses a backend from a Kind string — local (in-process, dev
// / replica=1) or redis (cross-pod, staging/prod) — mirroring the
// foundation/logger.NewFromConfig and clients/cache factory pattern, and wraps
// it in a Locker (NewLocker). The redis backend reuses the live go-redis
// client from the cache (clients/cache/redis.Cache.Client), so no second
// connection pool is opened.
//
// Hold acquires a key and keeps its lease alive with a background watchdog;
// the context it returns is cancelled the instant the lease is lost, so a
// losing pod's in-flight work is stopped rather than left running under a
// lock it no longer owns. Key naming is the caller's.
//
// Cross-cutting concerns are composed by the decorators subpackage
// (resilience: retry, circuit-breaker, timeout; observability: logging,
// metrics, tracing), wired by NewFromConfig from the Config collaborators.
//
// Scope boundary: this package ships both backends, the factory, the decorator
// stack, and Hold. Wiring them (config + shared redis client +
// observability/resilience deps → Locker) and choosing the call sites and key
// names belong to the consumer's composition root.
package lock

import (
	"context"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/local"
	lockredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Kind selects which DistributedLock backend NewFromConfig builds.
type Kind int

const (
	// KindLocal is the in-process backend: correct for a single replica (dev /
	// replica=1), no external dependency.
	KindLocal Kind = iota

	// KindRedis is the cross-pod backend over a shared Redis client
	// (staging/prod, multi-replica).
	KindRedis
)

// String returns the config token for the kind.
func (k Kind) String() string {
	switch k {
	case KindLocal:
		return "local"
	case KindRedis:
		return "redis"
	default:
		return "unknown"
	}
}

// Config selects and tunes the lock. Kind picks the backend; TTL is the lease
// duration a holder gets on Acquire (and Renew extends); RenewInterval is how
// often Hold's watchdog renews. RenewInterval must be ≤ TTL/3 so a couple of
// missed renew ticks cannot expire a live lease.
type Config struct {
	// Logger drives the logging decorator; nil skips it.
	Logger interfaces.Logger

	// Metrics drives the metrics decorator; nil skips it.
	Metrics interfaces.Metrics

	// Tracer drives the tracing decorator; nil skips it.
	Tracer interfaces.Tracer

	// Retrier drives the retry decorator (transient-error retries); nil skips it.
	Retrier interfaces.Retrier

	// CircuitBreaker drives the circuit-breaker decorator; nil skips it.
	CircuitBreaker interfaces.CircuitBreaker

	// Name labels the lock in metrics, spans, and logs (default "lock").
	Name string

	// Kind selects the backend (local | redis).
	Kind Kind

	// TTL is the lease duration applied on Acquire and extended by Renew.
	TTL time.Duration

	// RenewInterval is the watchdog's renew cadence in Hold.
	RenewInterval time.Duration

	// OpTimeout bounds each backend operation via the timeout decorator; zero
	// skips it.
	OpTimeout time.Duration
}

// NewFromConfig builds the configured Locker: it selects a DistributedLock
// backend by Kind, wraps it in the resilience + observability decorator stack
// (from the Config collaborators — each optional/nil-safe), and returns a
// Locker whose Hold renews on RenewInterval over the decorated backend. The
// client is required only for KindRedis (the shared go-redis client reused
// from the cache); it is ignored for KindLocal. It fails loudly on an unknown
// kind, a non-positive TTL or RenewInterval, a RenewInterval that exceeds
// TTL/3, or a redis kind with no client.
func NewFromConfig(
	cfg *Config,
	client *goredis.Client,
) (interfaces.Locker, error) {
	if cfg.TTL <= 0 {
		return nil, apperr.New(apperr.CodeInvalidInput, "lock ttl must be positive")
	}
	if cfg.RenewInterval <= 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"lock renew_interval must be positive",
		)
	}
	if cfg.RenewInterval*3 > cfg.TTL {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"lock renew_interval must be <= ttl/3",
		)
	}

	var backend interfaces.DistributedLock
	switch cfg.Kind {
	case KindLocal:
		backend = local.New()
	case KindRedis:
		if client == nil {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"redis lock requires a redis client",
			)
		}
		backend = lockredis.New(client, cfg.TTL)
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"unknown lock kind: "+cfg.Kind.String(),
		)
	}

	name := cfg.Name
	if name == "" {
		name = "lock"
	}
	// Wrap the backend in the cross-cutting decorator stack (resilience +
	// observability); each collaborator is nil-safe, so an unconfigured Config
	// yields the bare backend. Hold then composes over the decorated lock.
	decorated := decorators.NewBuilder(backend, name).
		WithRetry(cfg.Retrier).
		WithCircuitBreaker(cfg.CircuitBreaker).
		WithTimeout(cfg.OpTimeout).
		WithLogging(cfg.Logger).
		WithMetrics(cfg.Metrics).
		WithTracing(cfg.Tracer).
		Build()
	return NewLocker(decorated, cfg.RenewInterval), nil
}

// locker composes a DistributedLock backend with a renewal cadence to provide
// Hold; it is the interfaces.Locker implementation NewFromConfig returns. The
// embedded DistributedLock promotes Acquire/Renew/Release, so a locker is also
// a DistributedLock.
type locker struct {
	// DistributedLock is the wrapped backend; it promotes
	// Acquire/Renew/Release, so a locker is also a DistributedLock.
	interfaces.DistributedLock

	// renewInterval is how often Hold's watchdog renews the lease.
	renewInterval time.Duration
}

// compile-time assertion that *locker satisfies the consumer seam.
var _ interfaces.Locker = (*locker)(nil)

// NewLocker wraps a DistributedLock with the renewal cadence Hold uses,
// yielding a Locker. Exposed so callers (and tests) can compose Hold over any
// DistributedLock — e.g. a mock or a backend built directly; NewFromConfig
// builds one over the config-selected backend.
func NewLocker(
	lock interfaces.DistributedLock,
	renewInterval time.Duration,
) interfaces.Locker {
	return &locker{DistributedLock: lock, renewInterval: renewInterval}
}

// Hold implements interfaces.Locker. It acquires key and, on success, keeps
// the lease alive with a background watchdog that renews every renewInterval,
// cancelling the returned context the instant the lease is lost. See the
// interfaces.Locker.Hold contract for the return semantics.
func (l *locker) Hold(
	ctx context.Context,
	key string,
) (held context.Context, release func(), acquired bool, err error) {
	token, ok, err := l.Acquire(ctx, key)
	if err != nil {
		return canceledContext(ctx), func() {}, false, err
	}
	if !ok {
		return canceledContext(ctx), func() {}, false, nil
	}

	heldCtx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	var once sync.Once
	releaseFn := func() {
		once.Do(func() {
			close(stop)
			cancel()
			// Free the key on a context detached from ctx's cancellation, so a
			// completed request (ctx cancelled) still releases the lock.
			_ = l.Release(context.WithoutCancel(ctx), key, token)
		})
	}

	go func() {
		ticker := time.NewTicker(l.renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-heldCtx.Done():
				return
			case <-ticker.C:
				stillHeld, renewErr := l.Renew(heldCtx, key, token)
				if renewErr != nil || !stillHeld {
					// Lease lost: cancel held so the in-flight work stops. Do
					// not Release — we no longer own the key (release, when
					// the caller defers it, is a token-fenced no-op).
					cancel()
					return
				}
			}
		}
	}()

	return heldCtx, releaseFn, true, nil
}

// canceledContext returns a child of ctx that is already cancelled, so the
// held-context return value is never nil even on the not-acquired / error
// paths (where the caller must not use it).
func canceledContext(ctx context.Context) context.Context {
	c, cancel := context.WithCancel(ctx)
	cancel()
	return c
}
