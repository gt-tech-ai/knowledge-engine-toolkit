package interfaces

import "context"

// OpService is the generic operation-service contract implemented by CLI
// services. Unlike Service[T,P,ID] (entity CRUD), an OpService exposes a single
// decoratable entrypoint: a caller runs one of the service's operations by
// passing typed Args and receiving a typed Result. Multi-operation services
// model their operations as a tagged union in Args (an operation discriminator
// plus per-operation fields) and switch on it inside Run.
//
// A is the operation argument type (often a tagged union for multi-op services);
// R is the operation result type.
//
// Result-type contract. An OpService's R is one of exactly two archetypes, so
// operation services stay uniform:
//
//   - types.StepResults — GATE-PRODUCING: the operation fans out into per-item steps
//     that a gate renders (lint, test suites, build, format, security, …). The
//     error channel signals a hard failure; the steps carry per-item pass/fail/warn.
//   - struct{} — CMD-DIRECT: the operation has no per-item breakdown; success/failure
//     is carried entirely in Run's error (codegen, migrate, check, …).
//
// Any other R is drift to be migrated to one of these two.
type OpService[A any, R any] interface {
	// Name returns the service name, used as a label in logs, metrics, and
	// decorator actions.
	Name() string

	// Run executes the operation described by args and returns its result. It is
	// the single decoration chokepoint: cross-cutting concerns (logging, metrics,
	// tracing, recovery) wrap Run rather than each individual operation.
	Run(ctx context.Context, args A) (R, error)
}
