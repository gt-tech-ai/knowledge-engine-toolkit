package openapi

import (
	"cmp"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Violation is one REST RPC the completeness gate rejects: its fully-qualified name
// and the human-readable reason it could not be classified into a JSON:API role (and
// is not exempt). It is the output of CheckCompleteness — a completeness gate
// turns a non-empty slice into a loud, named failure.
type Violation struct {
	// RPC is the fully-qualified method name (e.g.
	// "library.v1.BookService.MarkRead").
	RPC string
	// Reason is why the RPC is unclassifiable — the classifier's message, which
	// names the RPC, method, and path so the failure points at the exact contract.
	Reason string
}

// CheckCompleteness walks the same REST surface as BuildRouteTable over a marshaled
// FileDescriptorSet and returns EVERY REST RPC that cannot be classified into a
// JSON:API role and is not exempt — the collect-all sibling of BuildRouteTable's
// fail-fast behavior, so a completeness gate reports the whole backlog in one run
// instead of one offender at a time. It gates the identical surface the generator
// does (only files carrying (jsonapi.v1.service); only methods with a
// google.api.http binding), so the gate and the generator never disagree.
//
// A structural/reader error (unparseable descriptor, malformed annotation, custom
// binding) is returned as the error result, not a Violation — it is a build defect
// the generator also rejects, distinct from a completeness gap. Violations are
// sorted by RPC for deterministic output.
func CheckCompleteness(
	fdsBytes []byte,
	exempt map[string]bool,
) ([]Violation, error) {
	files, types, err := loadDescriptorTypes(fdsBytes)
	if err != nil {
		return nil, err
	}

	// An empty descriptor set has no REST surface to gate (the no-op input, e.g. an
	// orchestration test's stub); return no violations rather than erroring on the
	// absent extension definitions.
	if files.NumFiles() == 0 {
		return nil, nil
	}

	exts, err := resolveJSONAPIExtensions(types)
	if err != nil {
		return nil, err
	}

	var violations []Violation

	err = exts.walkSurface(
		files,
		func(_ serviceOpt, md protoreflect.MethodDescriptor) error {
			found, opErr := methodViolations(md, exts, exempt)
			violations = append(violations, found...)

			return opErr
		},
	)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(
		violations,
		func(a, b Violation) int { return cmp.Compare(a.RPC, b.RPC) },
	)

	return violations, nil
}

// methodViolations returns a Violation for each of a method's HTTP bindings that
// Classify rejects and whose RPC is not exempt. It mirrors routeEntriesForMethod's
// surface gating and reader-error handling exactly (a gRPC-only method yields none;
// a malformed annotation is a returned error, not a violation) — the only
// difference is that it records the classification failure instead of aborting, so
// the gate collects all offenders.
func methodViolations(
	md protoreflect.MethodDescriptor,
	exts *jsonAPIExtensions,
	exempt map[string]bool,
) ([]Violation, error) {
	routes, err := exts.methodRoutes(md)
	if err != nil {
		return nil, err
	}

	var out []Violation

	for _, b := range routes.bindings {
		_, err := Classify(new(routes.binding(b)))
		if err == nil || exempt[routes.rpc] {
			continue // classified, or a genuine action/internal RPC
		}

		out = append(out, Violation{RPC: routes.rpc, Reason: err.Error()})
	}

	return out, nil
}
