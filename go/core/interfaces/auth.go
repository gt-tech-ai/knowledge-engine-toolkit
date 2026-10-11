package interfaces

// ServiceTokenValidator validates a service-to-service bearer token and returns the
// identity of the calling service. It is the swappable Strategy behind the internal
// service-auth interceptor: the engine ships a static per-caller token validator, and
// a workload-identity validator (K8s SA projected tokens / mesh mTLS) can replace it
// without touching the interceptor or its call sites.
type ServiceTokenValidator interface {
	// Validate returns the calling service's name for a valid token, or ok=false when
	// the token matches no known caller.
	Validate(token string) (caller string, ok bool)
}
