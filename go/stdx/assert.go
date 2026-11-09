package stdx

// As returns v as a T, or T's zero value when v does not hold one: the comma-ok
// assertion for call sites where absence and a wrong type mean the same thing.
func As[T any](v any) T {
	out, ok := v.(T)
	if !ok {
		var zero T

		return zero
	}

	return out
}
