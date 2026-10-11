// Package store (testutil) provides a interfaces.Store implementation that
// supports call counting and error injection for use in tests.
package store

import (
	"context"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// RecordingStore is a generic test helper that implements interfaces.Store and
// supports call counting plus error injection.
type RecordingStore[T any, P any, ID comparable] struct {
	// errAlways is the permanent error returned by every call when set; it takes
	// precedence over the transient errors sequence.
	errAlways error
	// GetFn overrides Get when non-nil.
	GetFn func(ctx context.Context, id ID) (*T, error)
	// ListFn overrides List when non-nil.
	ListFn func(ctx context.Context, params P, page types.PageRequest) (*types.Page[T], error) //nolint:lll // can't be multiple lines
	// CreateFn overrides Create when non-nil.
	CreateFn func(ctx context.Context, entity *T) (*T, error)
	// UpdateFn overrides Update when non-nil.
	UpdateFn func(ctx context.Context, id ID, entity *T) (*T, error)
	// DeleteFn overrides Delete when non-nil.
	DeleteFn func(ctx context.Context, id ID) error
	// ExistsFn overrides Exists when non-nil.
	ExistsFn func(ctx context.Context, id ID) (bool, error)
	// errors is the queue of transient errors returned one per call (FIFO).
	errors []error
	// calls is the total number of calls across all methods.
	calls int
	// mu guards calls and errors.
	mu sync.Mutex
	// requireDeadline, when true, makes every call fail unless ctx has a deadline.
	requireDeadline bool
}

// NewRecordingStore creates a RecordingStore with default behaviors.
func NewRecordingStore[T, P any, ID comparable]() *RecordingStore[T, P, ID] {
	return &RecordingStore[T, P, ID]{}
}

// CallCount returns the total number of calls across all methods.
func (s *RecordingStore[T, P, ID]) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// RequireDeadline enforces that a context deadline is present on every call.
func (s *RecordingStore[T, P, ID]) RequireDeadline(required bool) {
	s.requireDeadline = required
}

// SetTransientErrors configures the store to return a sequence of errors.
func (s *RecordingStore[T, P, ID]) SetTransientErrors(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = errs
	s.errAlways = nil
}

// SetPermanentError configures the store to always return the given error.
func (s *RecordingStore[T, P, ID]) SetPermanentError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errAlways = err
}

// Reset clears call counts and error state.
func (s *RecordingStore[T, P, ID]) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = 0
	s.errors = nil
	s.errAlways = nil
	// requireDeadline is intentionally preserved.
}

// Get returns the configured result or the zero value of T.
func (s *RecordingStore[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	if err := s.beforeCall(ctx); err != nil {
		return nil, err
	}
	if s.GetFn != nil {
		return s.GetFn(ctx, id)
	}
	var zero T
	return &zero, nil
}

// List returns the configured result or an empty page.
func (s *RecordingStore[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	if err := s.beforeCall(ctx); err != nil {
		return nil, err
	}
	if s.ListFn != nil {
		return s.ListFn(ctx, params, page)
	}
	return &types.Page[T]{}, nil
}

// Create returns the configured result or the zero value of T.
func (s *RecordingStore[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	if err := s.beforeCall(ctx); err != nil {
		return nil, err
	}
	if s.CreateFn != nil {
		return s.CreateFn(ctx, entity)
	}
	var zero T
	return &zero, nil
}

// Update returns the configured result or the zero value of T.
func (s *RecordingStore[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	if err := s.beforeCall(ctx); err != nil {
		return nil, err
	}
	if s.UpdateFn != nil {
		return s.UpdateFn(ctx, id, entity)
	}
	var zero T
	return &zero, nil
}

// Delete returns the configured error or nil.
func (s *RecordingStore[T, P, ID]) Delete(ctx context.Context, id ID) error {
	if err := s.beforeCall(ctx); err != nil {
		return err
	}
	if s.DeleteFn != nil {
		return s.DeleteFn(ctx, id)
	}
	return nil
}

// Exists returns the configured result or true.
func (s *RecordingStore[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	if err := s.beforeCall(ctx); err != nil {
		return false, err
	}
	if s.ExistsFn != nil {
		return s.ExistsFn(ctx, id)
	}
	return true, nil
}

// beforeCall applies common per-call behavior: optional deadline enforcement
// plus error injection and call counting.
func (s *RecordingStore[T, P, ID]) beforeCall(ctx context.Context) error {
	if s.requireDeadline {
		if _, ok := ctx.Deadline(); !ok {
			return apperr.Internal("missing deadline")
		}
	}

	return s.nextError()
}

// nextError increments the call count and returns the next injected error, if any.
// Permanent errors take precedence over transient sequences.
func (s *RecordingStore[T, P, ID]) nextError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.errAlways != nil {
		return s.errAlways
	}
	if len(s.errors) == 0 {
		return nil
	}
	err := s.errors[0]
	s.errors = s.errors[1:]
	return err
}

// Compile-time assertion that *RecordingStore satisfies interfaces.Store.
var _ interfaces.Store[any, any, int] = (*RecordingStore[any, any, int])(nil)
