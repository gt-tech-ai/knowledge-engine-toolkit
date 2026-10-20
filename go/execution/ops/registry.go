package ops

import (
	"context"
	"fmt"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Registry holds the operations one front-end's exposure policy admits. Build it with
// NewFromConfig. Operations share the working tree, so the registry runs one at a time:
// a single slot, held by a named holder, that a second run fails fast against.
type Registry struct {
	// known is every described operation, admitted or not, so a lookup can tell an
	// unknown name from a forbidden one.
	known map[string]bool
	// ops maps each admitted operation's name to it.
	ops map[string]interfaces.Operation
	// chains maps each admitted operation's name to its decorator chain.
	chains map[string]decorate.OpMiddleware
	// holder names who holds the working-tree slot; empty when it is free.
	holder string
	// list is the admitted operations sorted by name.
	list []interfaces.Operation
	// mu guards holder.
	mu sync.Mutex
}

// Reservation holds the registry's working-tree slot for one run of one operation.
// It is one-shot: Run releases the slot when the operation returns, and a Reservation
// that has run or been released cannot run again.
type Reservation struct {
	// reg is the registry whose slot is held.
	reg *Registry
	// name is the reserved operation.
	name string
	// once releases the slot exactly once.
	once sync.Once
	// mu guards done.
	mu sync.Mutex
	// done marks a Reservation that has started its Run or been released.
	done bool
}

// List returns the admitted operations sorted by name.
func (r *Registry) List() []interfaces.Operation {
	return append([]interfaces.Operation(nil), r.list...)
}

// Lookup returns the admitted operation named name; ok is false when the name is
// unknown or the policy excludes it.
func (r *Registry) Lookup(name string) (interfaces.Operation, bool) {
	op, ok := r.ops[name]
	return op, ok
}

// Find returns the admitted operation named name, failing like Run when it is unknown
// (CodeNotFound) or excluded by the policy (CodeForbidden).
func (r *Registry) Find(name string) (interfaces.Operation, error) {
	if op, ok := r.ops[name]; ok {
		return op, nil
	}
	return nil, r.missing(name)
}

// Run reserves the working-tree slot and runs the admitted operation named name with a
// decoded input under its decorator chain. An undescribed name fails with
// CodeNotFound, a described name the policy excludes with CodeForbidden, and a held
// slot with CodeUnavailable naming its holder.
func (r *Registry) Run(
	ctx context.Context, name string, input any, obs interfaces.ExecutionObserver,
) error {
	res, err := r.Reserve(name, name)
	if err != nil {
		return err
	}
	return res.Run(ctx, input, obs)
}

// Reserve takes the working-tree slot for the admitted operation named name on behalf
// of holder (the operation's name, or a background run's id). It fails like Run for an
// unknown or excluded name, with CodeInvalidInput for an empty holder, and with
// CodeUnavailable "busy: <holder> is running" when the slot is held.
func (r *Registry) Reserve(name, holder string) (interfaces.Reservation, error) {
	if _, ok := r.ops[name]; !ok {
		return nil, r.missing(name)
	}
	if holder == "" {
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"reserve operation %q: holder is empty", name,
		))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holder != "" {
		return nil, apperr.New(
			apperr.CodeUnavailable,
			fmt.Sprintf("busy: %s is running", r.holder),
		)
	}
	r.holder = holder
	return &Reservation{reg: r, name: name}, nil
}

// Run runs the reserved operation under its decorator chain and then releases the
// slot. A nil ctx runs under context.Background(). A second call, or a call after
// Release, fails with CodeInternal and runs nothing.
func (s *Reservation) Run(
	ctx context.Context, //nolint:contextcheck // nil-ctx fallback: no parent to inherit
	input any,
	obs interfaces.ExecutionObserver,
) error {
	if !s.claim() {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"reservation for operation %q already used or released", s.name,
		))
	}
	defer s.free()
	if ctx == nil {
		ctx = context.Background()
	}
	return s.reg.exec(ctx, s.name, input, obs)
}

// Release frees the slot without running, so a later Run fails; calling it again, or
// after Run, is a no-op.
func (s *Reservation) Release() {
	s.claim()
	s.free()
}

// claim marks the reservation done, reporting whether this call was the first to.
func (s *Reservation) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	s.done = true
	return true
}

// free clears the registry's holder exactly once.
func (s *Reservation) free() {
	s.once.Do(func() {
		s.reg.mu.Lock()
		s.reg.holder = ""
		s.reg.mu.Unlock()
	})
}

// exec runs the admitted operation named name under its decorator chain.
func (r *Registry) exec(
	ctx context.Context, name string, input any, obs interfaces.ExecutionObserver,
) error {
	op := r.ops[name]
	_, err := decorate.Exec(
		ctx,
		r.chains[name],
		name,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, op.Run(ctx, input, obs)
		},
	)
	return err
}

// missing explains why name is not runnable: unknown, or excluded by the policy.
func (r *Registry) missing(name string) error {
	if r.known[name] {
		return apperr.New(apperr.CodeForbidden, fmt.Sprintf(
			"operation %q is not exposed by this front-end's policy", name,
		))
	}
	return apperr.New(apperr.CodeNotFound, fmt.Sprintf("unknown operation %q", name))
}
