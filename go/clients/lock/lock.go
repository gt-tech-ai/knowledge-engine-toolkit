// Package lock builds a config-selected core/interfaces.Locker — a
// DistributedLock (keyed, token-fenced, best-effort) plus Hold, the managed
// acquire-with-watchdog helper a single-active-operation guard depends on.
//
// NewFromConfig chooses a backend from a Kind string — local (in-process, dev
// / replica=1; the default) or redis (cross-pod, staging/prod) — and wraps it in a
// Locker (NewLocker). The redis backend reuses a shared go-redis client (Deps.Redis),
// so no second connection pool is opened.
//
// Hold acquires a key and keeps its lease alive with a background watchdog;
// the context it returns is cancelled the instant the lease is lost, so a
// losing pod's in-flight work is stopped rather than left running under a
// lock it no longer owns. Key naming is the caller's.
//
// Every backend operation runs through the client tier (Decorate), wired by
// NewFromConfig from the Config collaborators.
//
// Scope boundary: this package ships both backends, the factory, the client-tier
// decoration, and Hold. Wiring them (config + shared redis client +
// observability/resilience deps → Locker) and choosing the call sites and key
// names belong to the consumer's composition root.
package lock

import (
	"context"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/local"
	lockredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindLocal is the in-process backend: correct for a single replica (dev /
	// replica=1), no external dependency. The default.
	KindLocal client.Kind = "local"

	// KindRedis is the cross-pod backend over a shared Redis client
	// (staging/prod, multi-replica).
	KindRedis client.Kind = "redis"
)

// Config selects and tunes the lock. TTL is the lease a holder gets on Acquire (and
// Renew extends); RenewInterval is how often Hold's watchdog renews, at most TTL/3 so a
// couple of missed renew ticks cannot expire a live lease.
type Config struct {
	// Kind selects the backend; empty means KindLocal.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("lock").
	Name string `yaml:"name" mapstructure:"name"`

	// TTL is the lease duration applied on Acquire and extended by Renew.
	TTL time.Duration `yaml:"ttl" mapstructure:"ttl"`

	// RenewInterval is the watchdog's renew cadence in Hold.
	RenewInterval time.Duration `yaml:"renew_interval" mapstructure:"renew_interval"`
}

// Deps carries the lock's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Redis is the shared go-redis client KindRedis requires.
	Redis *goredis.Client
}

// kinds is the lock backend table.
func kinds() client.Kinds[Config, Deps, interfaces.DistributedLock] {
	return client.Kinds[Config, Deps, interfaces.DistributedLock]{
		Family:   "lock",
		Default:  KindLocal,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.DistributedLock]{
			KindLocal: func(context.Context, Config, Deps) (interfaces.DistributedLock, error) {
				return local.New(), nil
			},
			KindRedis: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.DistributedLock, error) {
				if deps.Redis == nil {
					return nil, apperr.New(
						apperr.CodeInvalidInput,
						"redis lock requires a redis client",
					)
				}
				return lockredis.New(deps.Redis, cfg.TTL), nil
			},
		},
	}
}

// NewFromConfig builds the configured Locker: the backend cfg.Kind selects, run
// through the client tier, with Hold renewing on RenewInterval. A non-positive TTL or
// RenewInterval, a RenewInterval above TTL/3, an unknown kind, or KindRedis without
// Deps.Redis is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
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
	lock, err := kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
	if err != nil {
		return nil, err
	}
	return NewLocker(lock, cfg.RenewInterval), nil
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
