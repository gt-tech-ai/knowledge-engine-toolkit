package fixtures

// Build returns a T holding only what set assigns, so a table row names the fields its
// test is about and leaves the rest zero.
func Build[T any](set func(*T)) T {
	var value T

	set(&value)

	return value
}

// BuildRef is Build for a row held by pointer.
func BuildRef[T any](set func(*T)) *T {
	value := new(T)

	set(value)

	return value
}
