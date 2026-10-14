package lock

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every operation of base through the client tier as the client named
// name.
func Decorate(
	base interfaces.DistributedLock,
	name string,
	deps decorate.Deps,
) interfaces.DistributedLock {
	return &decorated{base: base, Base: client.NewBase(name, deps)}
}

// decorated runs each DistributedLock operation through the client tier.
type decorated struct {
	// base is the wrapped lock.
	base interfaces.DistributedLock

	// Base runs the operations.
	client.Base
}

var _ interfaces.DistributedLock = (*decorated)(nil)

// lease is one Acquire result carried through Exec.
type lease struct {
	// token fences the lease.
	token string

	// acquired reports whether the key was taken.
	acquired bool
}

// Acquire takes key as the operation "acquire".
func (d *decorated) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	l, err := d.Exec(ctx, "acquire", func(ctx context.Context) (lease, error) {
		t, ok, err := d.base.Acquire(ctx, key)
		return lease{token: t, acquired: ok}, err
	})
	return l.token, l.acquired, err
}

// Renew extends the lease on key as the operation "renew".
func (d *decorated) Renew(ctx context.Context, key, token string) (bool, error) {
	return d.Exec(ctx, "renew", func(ctx context.Context) (bool, error) {
		return d.base.Renew(ctx, key, token)
	})
}

// Release frees key as the operation "release".
func (d *decorated) Release(ctx context.Context, key, token string) error {
	return d.Do(ctx, "release", func(ctx context.Context) error {
		return d.base.Release(ctx, key, token)
	})
}
