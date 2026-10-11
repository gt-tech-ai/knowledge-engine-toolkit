package interfaces

// Validatable is a DTO that can validate itself — the below-transport-edge seam the
// service chokepoint enforces. Its Validate() returns a core/errors.AppError carrying
// CodeInvalidInput (ARCHITECTURE.md#error-codes) on violation, or nil when the DTO
// satisfies its rules. The generated per-DTO Validate() (protoc-gen-dtovalidate)
// satisfies this interface, composing the field checks with an optional hand-written
// domainRules() hook.
type Validatable interface {
	// Validate reports whether the DTO satisfies its field + cross-field rules, returning
	// the first violation as a coded (CodeInvalidInput) error, or nil when every rule is
	// satisfied.
	Validate() error
}
