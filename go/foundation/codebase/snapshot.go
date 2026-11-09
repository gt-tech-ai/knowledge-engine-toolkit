package codebase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
	return &Snapshot{key: key, entries: map[string]*entry{}, mu: sync.Mutex{}}
}

// Key returns the snapshot's identity.
func (s *Snapshot) Key() string { return s.key }

// Keys returns the memo keys the snapshot holds, sorted: every build that finished with
// a value or a stored error (a cancelled or panicking build leaves no key). It is the
// record of what a run computed, so a test can assert a view was built once.
func (s *Snapshot) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Sorted(maps.Keys(s.entries))
}

// Memo returns the value build computes for key, running build once per key: callers that
// arrive while it runs wait for it (or for their own ctx), and later callers get the
// stored value. A build error is stored and returned to every caller, except a context
// cancellation or deadline, which is returned to its caller only so the next caller
// builds afresh. A build that panics is CodeInternal for its caller and the callers
// waiting on it, and is not stored, so a later caller builds again. A key reused with a
// different T is CodeInternal.
func (s *Snapshot) Memo[T any](
	ctx context.Context,
	key string,
	build func(context.Context) (T, error),
) (T, error) {
	var zero T

	for {
		secondEntry, owner := s.claim(key)
		if owner {
			return memoBuild(ctx, s, key, secondEntry, build)
		}

		err := memoAwait(ctx, key, secondEntry)
		if err != nil {
			return zero, err
		}

		if !secondEntry.cancelled { // otherwise the builder's context ended; build again
			return memoValue[T](key, secondEntry)
		}
	}
}

// claim returns key's entry, reporting whether this caller created it and must build.
func (s *Snapshot) claim(key string) (*entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[key]; ok {
		return e, false
	}

	e := &entry{done: make(chan struct{}), val: nil, err: nil, cancelled: false}
	s.entries[key] = e

	return e, true
}

// memoBuild runs build for the owner, forgetting a panicked or cancelled result so a
// later caller builds again.
func memoBuild[T any](
	ctx context.Context,
	snapshot *Snapshot,
	key string,
	currentEntry *entry,
	build func(context.Context) (T, error),
) (T, error) {
	val, panicked, err := safeBuild(ctx, key, build)

	currentEntry.val, currentEntry.err = val, err
	currentEntry.cancelled = err != nil && !panicked && ctx.Err() != nil

	if panicked || currentEntry.cancelled {
		snapshot.mu.Lock()
		delete(snapshot.entries, key)
		snapshot.mu.Unlock()
	}

	close(currentEntry.done)

	return val, err
}

// memoAwait waits for another caller's build, or for ctx to end.
func memoAwait(ctx context.Context, key string, e *entry) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return apperr.Wrap(ctx.Err(), apperr.ContextCode(ctx.Err(),
			apperr.CodeTimeout), "memo "+key)
	}
}

// memoValue returns a finished entry's error, or its value as T.
func memoValue[T any](key string, currentEntry *entry) (T, error) {
	var zero T

	if currentEntry.err != nil {
		return zero, currentEntry.err
	}

	value, ok := currentEntry.val.(T)
	if !ok {
		return zero, apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"memo %q holds %T, not the requested type", key, currentEntry.val,
		))
	}

	return value, nil
}

// safeBuild runs build, turning a panic into a CodeInternal error carrying the panic
// value (the error records the stack), and reporting it, so a panicking build still
// closes its entry.
func safeBuild[T any](
	ctx context.Context,
	key string,
	build func(context.Context) (T, error),
) (val T, panicked bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			err = apperr.New(
				apperr.CodeInternal,
				fmt.Sprintf("memo %q: build panicked: %v", key, r),
			)
		}
	}()

	val, err = build(ctx)

	return val, false, err
}
