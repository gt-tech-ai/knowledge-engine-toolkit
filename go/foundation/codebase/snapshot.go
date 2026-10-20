package codebase

import (
	"context"
	"fmt"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Snapshot memoizes a run's views of one repository: every keyed value is computed once
// and shared by concurrent callers for the snapshot's life. A Snapshot is safe for
// concurrent use; construct it with NewSnapshot.
type Snapshot struct {
	// entries maps a memo key to its *entry.
	entries map[string]*entry

	// key identifies the snapshot (a run id, or the repository state it was taken at).
	key string

	// mu guards entries.
	mu sync.Mutex
}

// entry is one key's computation: done closes once val and err are set.
type entry struct {
	// val is the computed value.
	val any

	// err is the build's error.
	err error

	// done closes when the build finishes.
	done chan struct{}

	// cancelled marks a build that failed because its caller's context ended; the
	// entry is dropped and a waiter builds again.
	cancelled bool
}

// NewSnapshot returns an empty snapshot identified by key.
func NewSnapshot(key string) *Snapshot {
	return &Snapshot{key: key, entries: map[string]*entry{}}
}

// Key returns the snapshot's identity.
func (s *Snapshot) Key() string { return s.key }

// Memo returns the value build computes for key, running build once per key: callers that
// arrive while it runs wait for it (or for their own ctx), and later callers get the
// stored value. A build error is stored and returned to every caller, except a context
// cancellation or deadline, which is returned to its caller only so the next caller
// builds afresh. A key reused with a different T is CodeInternal.
func (s *Snapshot) Memo[T any](
	ctx context.Context,
	key string,
	build func(context.Context) (T, error),
) (T, error) {
	var zero T
	for {
		s.mu.Lock()
		e, ok := s.entries[key]
		if !ok {
			e = &entry{done: make(chan struct{})}
			s.entries[key] = e
			s.mu.Unlock()
			val, err := build(ctx)
			e.val, e.err = val, err
			if err != nil && ctx.Err() != nil {
				e.cancelled = true
				s.mu.Lock()
				delete(s.entries, key)
				s.mu.Unlock()
			}
			close(e.done)
			return val, err
		}
		s.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return zero, apperr.Wrap(ctx.Err(), apperr.ContextCode(ctx.Err(),
				apperr.CodeTimeout), "memo "+key)
		}
		if e.cancelled {
			continue // the builder's context ended; build again for this caller
		}
		if e.err != nil {
			return zero, e.err
		}
		v, ok := e.val.(T)
		if !ok {
			return zero, apperr.New(apperr.CodeInternal, fmt.Sprintf(
				"memo %q holds %T, not the requested type", key, e.val,
			))
		}
		return v, nil
	}
}
