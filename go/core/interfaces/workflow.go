package interfaces

import "context"

// Workflow is the generic business process orchestration interface for multi-step
// coordination. In = input type, Out = output type.
//
// Workflows coordinate multiple operations across services, pipelines, repositories, and
// caches. Unlike pipelines (stateless transforms), workflows orchestrate stateful
// business processes with conditional logic, error recovery, and cross-cutting concerns.
//
// Examples: a lookup workflow (check cache → run pipeline → cache result), ingestion
// workflow (parse → validate → transform → index), order fulfillment (verify → charge →
// ship → notify).
type Workflow[In any, Out any] interface {
	// Execute orchestrates a multi-step business process.
	// Context is provided for cancellation and tracing.
	// Implementations may be stateful and typically depend on services, repos, and caches.
	Execute(ctx context.Context, input In) (Out, error)
}
