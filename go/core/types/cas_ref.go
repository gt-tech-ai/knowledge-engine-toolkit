package types

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"

// RefKey names one mutable pointer to a hash: a name within a scope.
type RefKey struct {
	// Scope is the owner of the ref, e.g. an organization id.
	Scope string
	// Name is the ref's name within the scope, e.g. "current".
	Name string
}

// RefMove is one compare-and-swap of a ref from one hash to another.
type RefMove struct {
	// Key names the ref.
	Key RefKey
	// From is the hash the ref must hold now; the zero hash means the ref must not
	// exist yet (create).
	From Hash
	// To is the hash the ref holds after the move; never zero.
	To Hash
}

// Validate refuses a move that cannot change anything or names no ref: an empty scope
// or name, a zero To, or From equal to To.
func (m *RefMove) Validate() error {
	switch {
	case m.Key.Scope == "" || m.Key.Name == "":
		return apperr.New(apperr.CodeInvalidInput, "cas: ref scope and name are required")
	case m.To.IsZero():
		return apperr.New(apperr.CodeInvalidInput, "cas: ref move target is zero")
	case m.From == m.To:
		return apperr.New(apperr.CodeInvalidInput, "cas: ref move changes nothing")
	}

	return nil
}
