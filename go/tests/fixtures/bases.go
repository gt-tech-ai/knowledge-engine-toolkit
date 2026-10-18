package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorations returns the Decorations that resolves every component and operation to no
// layers, for tests that build a base of any tier undecorated (recovery only):
// fixtures.Must(service.NewBase[C](fixtures.Decorations(), "test")).
func Decorations() decorate.Decorations { return decorate.Fixed(decorate.Deps{}) }

// Must returns v, panicking when err is non-nil: a test's wiring of a constructor that
// returns (T, error), such as a base built from decorations, fails loudly.
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
