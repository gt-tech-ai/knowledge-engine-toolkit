package types

// Option represents an optional value.
type Option[T any] struct {
	// Value is the contained value, meaningful only when Valid is true.
	Value T

	// Valid indicates whether Value is present.
	Valid bool
}

// Some creates an Option with a value.
func Some[T any](v T) Option[T] {
	return Option[T]{Value: v, Valid: true}
}

// None creates an empty Option.
func None[T any]() Option[T] {
	return Option[T]{}
}

// OptionFromPtr creates an Option from a pointer.
//
// If the pointer is nil, the Option will be None
// Otherwise, the Option will be Some(T)
func OptionFromPtr[T any](v *T) Option[T] {
	if v != nil {
		return Some(*v)
	}
	return None[T]()
}

// Get returns the value and whether it exists.
func (o Option[T]) Get() (T, bool) {
	return o.Value, o.Valid
}

// UnwrapOr returns the value if present, otherwise the fallback.
func (o Option[T]) UnwrapOr(fallback T) T {
	if o.Valid {
		return o.Value
	}
	return fallback
}

// UnwrapOrElse returns the value if present, otherwise calls the fallback function to
// compute a value.
func (o Option[T]) UnwrapOrElse(fallback func() T) T {
	if o.Valid {
		return o.Value
	}
	return fallback()
}

// IsSome returns true if the Option contains a value.
func (o Option[T]) IsSome() bool {
	return o.Valid
}

// IsNone returns true if the Option does not contain a value.
func (o Option[T]) IsNone() bool {
	return !o.Valid
}

// ToPtr returns a pointer to the value if present, or nil if absent.
func (o Option[T]) ToPtr() *T {
	if o.Valid {
		return &o.Value
	}
	return nil
}

// SomeIfNotEmpty maps an optional string ("" → None) to an Option, used at the transport
// edge for optional proto string fields where an empty value means "absent" / "leave
// unchanged" rather than an explicit Some("").
func SomeIfNotEmpty(s string) Option[string] {
	if s == "" {
		return None[string]()
	}
	return Some(s)
}
