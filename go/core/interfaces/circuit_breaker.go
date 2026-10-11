package interfaces

// CircuitBreaker prevents cascading failures by detecting downstream failures
// and shedding load by failing fast without invoking the downstream.
//
// Implementations: gobreaker (default).
//
// All service code depends on this interface, never on a concrete circuit
// breaker library. Swap implementations via the circuitbreaker.New factory.
type CircuitBreaker interface {
	// Execute runs fn through the circuit breaker. Returns an error if the
	// circuit is open or fn fails.
	Execute(fn func() error) error
}
