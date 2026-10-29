package openapi

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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

	svcExt, _ := types.FindExtensionByName(jsonAPIServiceExtension)
	resExt, _ := types.FindExtensionByName(jsonAPIResourceExtension)
	opExt, _ := types.FindExtensionByName(jsonAPIOperationExtension)
	if svcExt == nil || resExt == nil || opExt == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"descriptor set is missing the %s / %s / %s extension definitions",
				jsonAPIServiceExtension,
				jsonAPIResourceExtension,
				jsonAPIOperationExtension,
			),
		)
	}

	var violations []Violation
	var walkErr error

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		_, ok, err := readServiceOption(fd, svcExt, types)
		if err != nil {
			walkErr = apperr.Wrap(err, apperr.CodeInternal, fd.Path())
			return false
		}
		if !ok {
			return true // not part of the JSON:API REST surface
		}

		svcs := fd.Services()
		for i := range svcs.Len() {
			methods := svcs.Get(i).Methods()
			for j := range methods.Len() {
				vs, err := methodViolations(methods.Get(j), resExt, opExt, types, exempt)
				if err != nil {
					walkErr = err
					return false
				}
				violations = append(violations, vs...)
			}
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(violations, func(i, j int) bool {
		return violations[i].RPC < violations[j].RPC
	})
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
	resExt, opExt protoreflect.ExtensionType,
	types *protoregistry.Types,
	exempt map[string]bool,
) ([]Violation, error) {
	bindings, err := methodHTTPBindings(md)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil // gRPC-only method — not a REST route
	}

	rpc := string(md.FullName())

	action, err := readOperationOption(md, opExt, types)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, rpc)
	}

	rc, hasRes, err := readResourceOption(md.Output(), resExt, types)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, rpc)
	}
	if hasRes && rc.resourceType == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"%s: response %q: (jsonapi.v1.resource) is missing required field 'type'",
				rpc, md.Output().Name(),
			),
		)
	}

	var out []Violation
	for _, b := range bindings {
		if _, err := Classify(Binding{
			RPC:           rpc,
			HTTPMethod:    b.verb,
			PathTemplate:  b.path,
			SingleKey:     rc.singleKey,
			CollectionKey: rc.collectionKey,
			Action:        action,
		}); err != nil {
			if exempt[rpc] {
				continue // a genuine action/internal RPC — not a violation
			}
			out = append(out, Violation{RPC: rpc, Reason: err.Error()})
		}
	}
	return out, nil
}
