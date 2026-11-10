package stdx

// Zero returns the zero value of T. It names "nothing to report" for a struct type,
// where an empty composite literal would read as a value with its fields forgotten.
func Zero[T any]() T {
	var zero T

	return zero
}
