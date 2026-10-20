package ops

import (
	"fmt"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Config is what a front-end's composition root supplies to build its registry. E is
// the front-end's environment: the collaborators every operation captures.
type Config[E any] struct {
	// Env is the injected collaborators every admitted operation captures; a front-end
	// resolves the policy's Prompter kind into it before building.
	Env E

	// Log receives the operations' run logs and recovered panics; nil logs nothing.
	Log interfaces.Logger

	// Builders maps each operation name to its implementation.
	Builders map[string]Builder[E]

	// Descriptors are the operation descriptors, one per builder.
	Descriptors []types.OperationDescriptor

	// Policy decides which operations the registry admits.
	Policy types.ExposurePolicy
}

// NewFromConfig builds the registry of the operations cfg.Policy admits. It fails with
// CodeInvalidInput when the policy or a descriptor is invalid, when descriptors and
// builders don't pair one-to-one, or when the policy's allow/deny names an unknown
// operation. Building does no I/O.
func NewFromConfig[E any](cfg Config[E]) (*Registry, error) {
	if err := cfg.Policy.Validate("policy"); err != nil {
		return nil, err
	}
	known, err := pairDescriptors(cfg.Descriptors, cfg.Builders)
	if err != nil {
		return nil, err
	}
	if err := checkPolicyNames(cfg.Policy, known); err != nil {
		return nil, err
	}

	reg := &Registry{
		known:  known,
		ops:    map[string]interfaces.Operation{},
		chains: map[string]decorate.OpMiddleware{},
	}
	for _, d := range cfg.Descriptors {
		if !admits(cfg.Policy, d) {
			continue
		}
		op := cfg.Builders[d.Name](d, cfg.Env)
		reg.ops[d.Name] = op
		reg.chains[d.Name] = decorators(d, cfg.Log)
		reg.list = append(reg.list, op)
	}
	slices.SortFunc(reg.list, func(a, b interfaces.Operation) int {
		return strings.Compare(a.Descriptor().Name, b.Descriptor().Name)
	})
	return reg, nil
}

// pairDescriptors validates every descriptor and checks that descriptors and builders
// name the same operations, returning the set of known names.
func pairDescriptors[E any](
	descs []types.OperationDescriptor, builders map[string]Builder[E],
) (map[string]bool, error) {
	known := make(map[string]bool, len(descs))
	for _, d := range descs {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if known[d.Name] {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"operation descriptor %q is declared twice", d.Name,
			))
		}
		if _, ok := builders[d.Name]; !ok {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"operation descriptor %q has no implementation", d.Name,
			))
		}
		known[d.Name] = true
	}
	names := make([]string, 0, len(builders))
	for name := range builders {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !known[name] {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"operation %q has no descriptor", name,
			))
		}
	}
	return known, nil
}
