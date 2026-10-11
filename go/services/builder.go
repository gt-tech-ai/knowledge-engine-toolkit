package services

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// RunFunc is a single service operation: it executes the operation described by
// args and returns its result. It is the unit the decorator middlewares wrap.
type RunFunc[A any, R any] func(ctx context.Context, args A) (R, error)

// Build starts a fluent builder that assembles a decorated OpService from the
// given operation function. Dependency injection happens before Build, by
// closing run over the service's repos; the builder only adds cross-cutting
// concerns (logging, recovery).
func Build[A, R any](run RunFunc[A, R]) *Builder[A, R] {
	return &Builder[A, R]{run: run}
}

// Builder assembles a decorated interfaces.OpService[A,R] from a single RunFunc.
// Decorators are applied inside-out: base run -> logging -> recovery (recovery
// is ALWAYS outermost).
type Builder[A any, R any] struct {
	// run is the innermost operation to decorate.
	run RunFunc[A, R]

	// log enables the logging decorator when non-nil and is used by recovery.
	log interfaces.Logger

	// name labels the service in log fields and decorator actions.
	name string
}

// Named sets the service name used in log fields and decorator actions.
func (b *Builder[A, R]) Named(name string) *Builder[A, R] {
	b.name = name
	return b
}

// WithLog adds a logging decorator that logs operation entry at Debug level and
// failures at Error level.
func (b *Builder[A, R]) WithLog(log interfaces.Logger) *Builder[A, R] {
	b.log = log
	return b
}

// Service constructs the decorated OpService. Decorators are applied inside-out:
// base run -> logging -> recovery (recovery is ALWAYS outermost, so it catches
// panics from every inner decorator and from the operation itself).
func (b *Builder[A, R]) Service() interfaces.OpService[A, R] {
	run := b.run
	if b.log != nil {
		run = withLogging(b.name, b.log, run)
	}
	run = withRecovery(b.name, b.log, run)
	return &opService[A, R]{run: run, name: b.name}
}

// compile-time check: opService satisfies the OpService contract.
var _ interfaces.OpService[any, any] = (*opService[any, any])(nil)

// opService is the decorated OpService[A,R] the Builder produces.
type opService[A any, R any] struct {
	// run is the fully-decorated operation chain.
	run RunFunc[A, R]

	// name labels the service, returned by Name.
	name string
}

// Name returns the service name.
func (s *opService[A, R]) Name() string { return s.name }

// Run executes the decorated operation chain.
func (s *opService[A, R]) Run(ctx context.Context, args A) (R, error) {
	return s.run(ctx, args)
}

// withLogging wraps run to log operation entry at Debug and failure at Error.
func withLogging[A, R any](
	name string,
	log interfaces.Logger,
	run RunFunc[A, R],
) RunFunc[A, R] {
	return func(ctx context.Context, args A) (R, error) {
		log.Debug("service.Run", "service", name)
		result, err := run(ctx, args)
		if err != nil {
			log.Error("service.Run failed", "service", name, "error", err)
		}
		return result, err
	}
}

// withRecovery wraps run to convert a panic into an error, logging it when a
// logger is present. It is always the outermost decorator.
func withRecovery[A, R any](
	name string,
	log interfaces.Logger,
	run RunFunc[A, R],
) RunFunc[A, R] {
	return func(ctx context.Context, args A) (result R, err error) {
		defer func() {
			if r := recover(); r != nil {
				if log != nil {
					log.Error("service.Run panicked", "service", name, "panic", r)
				}
				err = apperr.New(
					apperr.CodeInternal,
					fmt.Sprintf("service %s panicked: %v", name, r),
				)
			}
		}()
		return run(ctx, args)
	}
}
