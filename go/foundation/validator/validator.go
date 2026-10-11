// Package validator provides a small, generic business-rule validator for the
// service layer — the below-transport-boundary complement to the proto/request
// validation performed at the transport edge (connect ValidateInterceptor). It
// carries no backends and needs no Kind/factory: it is a pure composition
// mechanism over caller-supplied rules.
package validator

// Rule validates one aspect of a *T, returning a non-nil error on violation and
// nil when satisfied. Rules should return a core/errors.AppError carrying
// CodeInvalidInput (ARCHITECTURE.md#error-codes) so the failure classifies and maps to a
// 400 / InvalidArgument at the transport edge.
type Rule[T any] func(*T) error

// Validator runs an ordered set of Rules against a *T, short-circuiting on the
// first violation. The zero Validator (no rules) accepts everything.
type Validator[T any] struct {
	// rules are evaluated in order; the first non-nil error is returned.
	rules []Rule[T]
}

// New builds a Validator from the given rules, evaluated in the order supplied.
func New[T any](rules ...Rule[T]) *Validator[T] {
	return &Validator[T]{rules: rules}
}

// Validate runs each rule in order and returns the first violation, or nil if the
// entity satisfies every rule. Its signature matches the func the service builder's
// WithValidation accepts, so `WithValidation(v.Validate)` wires it in directly.
func (v *Validator[T]) Validate(entity *T) error {
	for _, rule := range v.rules {
		if err := rule(entity); err != nil {
			return err
		}
	}
	return nil
}
