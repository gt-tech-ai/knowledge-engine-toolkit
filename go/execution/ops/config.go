package ops

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
// builders don't pair one-to-one, when a builder is nil, when an admitted operation's
// builder returns no operation or seeds defaults for another input type, or when the
// policy's allow/deny names an unknown operation. Building does no I/O.
func NewFromConfig[E any](cfg Config[E]) (*Registry, error) {
	err := cfg.Policy.Validate("policy")
	if err != nil {
		return nil, err
	}

	known, err := pairDescriptors(cfg.Descriptors, cfg.Builders)
	if err != nil {
		return nil, err
	}

	err = checkPolicyNames(&cfg.Policy, known)
	if err != nil {
		return nil, err
	}

	reg := &Registry{
		known:  known,
		ops:    map[string]interfaces.Operation{},
		chains: map[string]decorate.OpMiddleware{},
		holder: "",
		list:   nil,
		mu:     sync.Mutex{},
	}

	err = addAdmitted(reg, &cfg)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(reg.list, func(a, b interfaces.Operation) int {
		return strings.Compare(a.Descriptor().Name, b.Descriptor().Name)
	})

	return reg, nil
}

// addAdmitted builds and registers each operation the policy admits.
func addAdmitted[E any](reg *Registry, cfg *Config[E]) error {
	for _, operationDescriptor := range cfg.Descriptors {
		if !admits(&cfg.Policy, &operationDescriptor) {
			continue
		}

		operation, err := build(
			cfg.Builders[operationDescriptor.Name],
			&operationDescriptor,
			cfg.Env,
		)
		if err != nil {
			return err
		}

		reg.ops[operationDescriptor.Name] = operation
		reg.chains[operationDescriptor.Name] = decorators(&operationDescriptor, cfg.Log)
		reg.list = append(reg.list, operation)
	}

	return nil
}

// build runs b for d and checks what it built: an operation, whose seeded defaults (if
// any) fit its input.
func build[E any](
	b Builder[E],
	operationDescriptor *types.OperationDescriptor,
	env E,
) (interfaces.Operation, error) {
	operation := b(*operationDescriptor, env)
	if operation == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: its builder returned no operation", operationDescriptor.Name,
		))
	}

	if c, ok := operation.(interface{ check() error }); ok {
		err := c.check()
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
				fmt.Sprintf("operation %q", operationDescriptor.Name))
		}
	}

	return operation, nil
}

// pairDescriptors validates every descriptor and checks that descriptors and builders
// name the same operations, returning the set of known names.
func pairDescriptors[E any](
	descs []types.OperationDescriptor, builders map[string]Builder[E],
) (map[string]bool, error) {
	known := make(map[string]bool, len(descs))

	for _, operationDescriptor := range descs {
		err := checkDescriptor(&operationDescriptor, known, builders)
		if err != nil {
			return nil, err
		}

		known[operationDescriptor.Name] = true
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

// checkDescriptor requires a valid, first-seen descriptor with an implementation.
func checkDescriptor[E any](
	operationDescriptor *types.OperationDescriptor,
	known map[string]bool,
	builders map[string]Builder[E],
) error {
	err := operationDescriptor.Validate()
	if err != nil {
		return err
	}

	if known[operationDescriptor.Name] {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation descriptor %q is declared twice", operationDescriptor.Name,
		))
	}

	if b, ok := builders[operationDescriptor.Name]; !ok || b == nil {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation descriptor %q has no implementation", operationDescriptor.Name,
		))
	}

	return nil
}
