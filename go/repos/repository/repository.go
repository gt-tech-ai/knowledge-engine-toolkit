// Package repository is the repository tier: a Base, built from the repository's
// contract, is the embeddable core of a consumer's repository, running its custom
// queries through the tier's decoration (Tier) with the promoted Exec. An Entity
// (entity.go) is a Base that also serves an entity's roles over its store, optionally
// beneath the read-through entity cache (cache.go).
package repository

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the repository tier's decoration: tracing → metrics → logging → timeout →
// circuit breaker → retry, outermost first. Retry never runs inside a transaction,
// and a Create never retries (it is not idempotent).
func Tier() decorate.Tier {
	return decorate.Tier{Name: "repository", Order: []decorate.Layer{
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerTimeout,
		decorate.LayerCircuitBreaker,
		decorate.LayerRetry,
	}}
}

// Base is the embeddable core of a repository named by its Subject: Name and Exec run a
// query through the tier's decoration, and ExecOnce runs a non-idempotent write the same
// way without retry.
// Each operation is a method of the repository's contract, decorated under its own name.
// The zero Base runs queries undecorated.
type Base struct {
	decorate.Subject
}

// NewBase builds the Base for the repository named name whose operations are the methods
// of its contract R (an interface), decorated from decs: the repository as the
// repository-tier component name, and each operation op as name+"."+op. A contract that
// is not an interface, or a decoration that fails to resolve, is an error.
func NewBase[R any](decs decorate.Decorations, name string) (Base, error) {
	subject, err := decs.SubjectOf[R](Tier(), name)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
}
