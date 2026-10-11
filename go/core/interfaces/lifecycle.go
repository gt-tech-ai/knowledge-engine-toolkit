package interfaces

import "context"

// Lifecycle is implemented by any client/component with coordinated startup and
// shutdown, so a lifecycle.Manager can Start them in registration order and Stop
// them in reverse for graceful shutdown.
type Lifecycle interface {
	// Start initializes the component and establishes its connection/resources. It
	// should block until the component is ready or ctx is cancelled.
	Start(ctx context.Context) error

	// Stop gracefully releases the component's resources and completes in-flight
	// work.
	Stop(ctx context.Context) error
}

// HealthChecker reports a component's health for Kubernetes probes: Liveness
// answers "is it alive?" (restart the pod if not) and Readiness "is it ready to
// serve?" (remove the pod from the load balancer if not).
type HealthChecker interface {
	// Liveness reports whether the component is alive (a lightweight self-check that
	// does not depend on external systems).
	Liveness(ctx context.Context) error

	// Readiness reports whether the component can serve traffic — typically by
	// verifying connectivity (e.g. a Ping).
	Readiness(ctx context.Context) error
}

// Client is the base contract for a resilient, lifecycle-managed client (gRPC,
// database pool, Connect server): it can be started/stopped by a lifecycle.Manager
// and health-probed by Kubernetes. It composes Lifecycle + HealthChecker.
type Client interface {
	// Lifecycle provides coordinated Start/Stop.
	Lifecycle
	// HealthChecker provides the Kubernetes Liveness/Readiness probes.
	HealthChecker
}
