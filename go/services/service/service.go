// Package service is the service tier: a Base, built from the service's contract, is the
// embeddable core of a consumer's service, running its custom operations through the
// tier's decoration (Tier) with the promoted Exec. Its role compositions (roles.go)
// decorate a repository's roles. Every service it builds is a core interface.
package service

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the service tier's decoration: recovery → tracing → metrics → logging →
// authorization → timeout, outermost first.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "service", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerLogging,
		decorate.LayerAuth,
		decorate.LayerTimeout,
	}}
}

// Base is the embeddable core of a service named by its Subject: Name and Exec run a
// domain operation through the tier's decoration, and the role compositions (CRUD,
// CRUDNoList, ReadListExist, Resource) decorate a repository's roles. Each operation is a
// method of the service's contract, decorated under its own name. The zero Base runs
// operations undecorated.
type Base struct {
	decorate.Subject
}

// NewBase builds the Base for the service named name whose operations are the methods of
// its contract S (an interface), decorated from decs: the service as the service-tier
// component name, and each operation op as name+"."+op. A contract that is not an
// interface, or a decoration that fails to resolve, is an error.
func NewBase[S any](decs decorate.Decorations, name string) (Base, error) {
	subject, err := decs.SubjectOf[S](Tier(), name)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
}
