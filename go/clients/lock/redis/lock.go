// Package redis provides a cross-pod DistributedLock backend over Redis, for
// multi-replica deployments (staging/prod). It satisfies the same
// core/interfaces.DistributedLock contract as the in-process local backend, so
// selecting it is a config change (lock.kind=redis), never a logic edit.
//
// Mechanism ('s Redis profile): Acquire is SET key token NX PX ttl — a
// single atomic set-if-absent with a lease; Renew and Release are Lua scripts
// that compare the stored token to the caller's before acting (PEXPIRE / DEL),
// so a previous holder can never renew or free the current holder's lock. Crash
// safety comes from the lease TTL: a holder that dies without releasing loses
// the key when the lease expires.
//
// Best-effort, NOT true mutual exclusion: a stalled holder past the TTL, or a
// Redis primary→replica failover that loses an unacknowledged SET, can admit a
// rare double-acquire (the Redlock critique). The clients/lock.Hold helper
// mitigates the stall case by cancelling the in-flight work when Renew reports
// the lease lost; callers must tolerate a rare duplicate.
package redis

import (
	"context"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// renewLua extends the lease iff the stored value still equals the caller's
// token. Returns 1 when renewed, 0 when the token no longer holds the key
// (expired, released, or taken over). KEYS[1]=key, ARGV[1]=token, ARGV[2]=ttlMs.
const renewLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
	return 0
end`

// releaseLua deletes the key iff the stored value still equals the caller's
// token (compare-then-DEL), so a stale Release from a previous holder cannot
// delete a new holder's lock. KEYS[1]=key, ARGV[1]=token.
const releaseLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end`

// Lock is a token-fenced, cross-pod DistributedLock over a shared go-redis
// client. The client is injected (reused from the cache — see
// clients/cache/redis.Cache.Client), so the lock opens no second connection pool.
type Lock struct {
	// client is the shared go-redis client the lock issues commands through.
	client *goredis.Client
	// renewScript runs renewLua (EVALSHA, falling back to EVAL).
	renewScript *goredis.Script
	// releaseScript runs releaseLua (EVALSHA, falling back to EVAL).
	releaseScript *goredis.Script
	// ttl is the lease duration applied on Acquire and extended on Renew.
	ttl time.Duration
}

// New returns a Redis DistributedLock over the given client with the given lease
// TTL. The client is expected to be the one shared with the cache; the lock does
// not own it and never closes it.
func New(client *goredis.Client, ttl time.Duration) *Lock {
	return &Lock{
		client:        client,
		ttl:           ttl,
		renewScript:   goredis.NewScript(renewLua),
		releaseScript: goredis.NewScript(releaseLua),
	}
}

// compile-time assertion that *Lock satisfies the seam.
var _ interfaces.DistributedLock = (*Lock)(nil)

// Acquire attempts SET key token NX PX ttl. On success it returns the fresh
// fencing token and acquired=true; if the key is already held it returns
// acquired=false. A Redis error is surfaced (coded UNAVAILABLE), distinct from a
// clean loss.
func (l *Lock) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", false, apperr.Wrap(err, apperr.CodeInternal, "generate lock token")
	}
	token = id.String()
	ok, err := l.client.SetNX(ctx, key, token, l.ttl).Result()
	if err != nil {
		return "", false, apperr.Wrap(err, apperr.CodeUnavailable, "acquire lock")
	}
	if !ok {
		return "", false, nil
	}
	return token, true, nil
}

// Renew extends the lease iff token still holds key (compare-then-PEXPIRE).
// held=false means the lease was lost; err is reserved for Redis failures.
func (l *Lock) Renew(ctx context.Context, key, token string) (held bool, err error) {
	res, err := l.renewScript.Run(ctx, l.client, []string{key}, token, l.ttl.Milliseconds()).
		Int64()
	if err != nil {
		return false, apperr.Wrap(err, apperr.CodeUnavailable, "renew lock")
	}
	return res == 1, nil
}

// Release frees key iff token still holds it (compare-then-DEL); a foreign token
// or an unheld key is a no-op. err is reserved for Redis failures.
func (l *Lock) Release(ctx context.Context, key, token string) error {
	if err := l.releaseScript.Run(ctx, l.client, []string{key}, token).Err(); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "release lock")
	}
	return nil
}
