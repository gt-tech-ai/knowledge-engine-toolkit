// Package local provides an in-process DistributedLock backend for single-pod
// deployments (dev / replica=1), where cross-process coordination is
// unnecessary. It satisfies the same core/interfaces.DistributedLock contract as
// the Redis backend, so selecting it is a config change (lock.kind=local), never
// a logic edit. Exclusion holds only within one process: with more than one
// replica, use the redis backend.
package local

import (
	"context"
	"sync"

	"github.com/google/uuid"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Lock is a token-fenced, in-process DistributedLock backed by a mutex-guarded
// map of held keys to their fencing tokens.
type Lock struct {
	// held maps a locked key to the fencing token of its current holder.
	held map[string]string

	// mu guards held so Acquire's check-then-set and Release/Renew's compare are
	// atomic under concurrent callers.
	mu sync.Mutex
}

// New returns an empty in-process DistributedLock.
func New() *Lock {
	return &Lock{held: make(map[string]string)}
}

// compile-time assertion that *Lock satisfies the seam.
var _ interfaces.DistributedLock = (*Lock)(nil)

// Acquire takes key if it is currently free, minting a fresh fencing token for
// the holder. If key is already held it returns acquired=false without blocking.
func (l *Lock) Acquire(
	_ context.Context,
	key string,
) (token string, acquired bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, busy := l.held[key]; busy {
		return "", false, nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", false, apperr.Wrap(err, apperr.CodeInternal, "generate lock token")
	}
	token = id.String()
	l.held[key] = token
	return token, true, nil
}

// Renew reports whether token still holds key. It never errors on the in-process
// backend (there is no lease to expire), so held=false means the key was
// released or is now held by a different token.
func (l *Lock) Renew(_ context.Context, key, token string) (held bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.held[key]
	return ok && cur == token, nil
}

// Release frees key only if token still holds it; a foreign token or an unheld
// key is a no-op, so a previous holder can never free the current one's lock.
func (l *Lock) Release(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.held[key]; ok && cur == token {
		delete(l.held, key)
	}
	return nil
}
