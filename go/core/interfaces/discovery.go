package interfaces

import "context"

// Discoverer discovers the set of work units for a job to iterate over.
type Discoverer[T any] interface {
	// Discover returns the work units found under root.
	Discover(ctx context.Context, root string) ([]T, error)
}

// DiscovererFunc is a function that implements Discoverer[T].
type DiscovererFunc[T any] func(ctx context.Context, root string) ([]T, error)

// Discover implements Discoverer[T] by calling the function.
func (f DiscovererFunc[T]) Discover(ctx context.Context, root string) ([]T, error) {
	return f(ctx, root)
}
