package infra

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// LockConfig selects and tunes the distributed lock that enforces at most one
// active operation per key across service replicas.
// The clients/lock factory reads Kind to pick a backend; TTL and RenewInterval
// tune the lease and the Hold watchdog. Which backend you get is a config change,
// not a code edit.
type LockConfig struct {
	// Kind selects the backend: "local" (in-process, dev / replica=1) or "redis"
	// (cross-pod, staging/prod).
	Kind string `mapstructure:"kind"`

	// TTL is the lease duration a holder receives on Acquire and Renew extends.
	TTL time.Duration `mapstructure:"ttl"`

	// RenewInterval is how often Hold's watchdog renews the lease. It must be
	// <= TTL/3 so a couple of missed renew ticks cannot expire a live lease.
	RenewInterval time.Duration `mapstructure:"renew_interval"`
}

// lockKindLocal is the in-process lock backend kind, the default.
const lockKindLocal = "local"

// DefaultLockConfig returns the in-process backend with a 30s lease renewed every
// 10s (renew_interval = ttl/3), matching the base config. Dev inherits this
// (local); staging/prod override kind to redis.
func DefaultLockConfig() LockConfig {
	return LockConfig{
		Kind:          lockKindLocal,
		TTL:           30 * time.Second,
		RenewInterval: 10 * time.Second,
	}
}

// Validate rejects an unknown kind, a non-positive TTL or RenewInterval, and a
// RenewInterval that exceeds TTL/3 (the invariant that keeps a live lease from
// expiring between renew ticks).
func (c LockConfig) Validate() error {
	switch c.Kind {
	case lockKindLocal, "redis":
	default:
		return apperr.InvalidInput("lock.kind must be \"local\" or \"redis\"")
	}
	if c.TTL <= 0 {
		return apperr.InvalidInput("lock.ttl must be > 0")
	}
	if c.RenewInterval <= 0 {
		return apperr.InvalidInput("lock.renew_interval must be > 0")
	}
	if c.RenewInterval*3 > c.TTL {
		return apperr.InvalidInput("lock.renew_interval must be <= lock.ttl/3")
	}
	return nil
}
