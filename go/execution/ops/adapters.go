package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/invopop/jsonschema"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Builder builds one operation from its descriptor over the front-end's environment.
type Builder[E any] func(desc types.OperationDescriptor, env E) interfaces.Operation

// funcOp is an operation whose input is a T and whose run is a function.
type funcOp[T, E any] struct {
	// env is the captured environment.
	env E

	// schema returns T's JSON Schema, reflected on first use.
	schema func() []byte

	// run executes the operation.
	run func(ctx context.Context, in *T, env E, obs interfaces.ExecutionObserver) error

	// desc is the operation's descriptor.
	desc types.OperationDescriptor
}

// Func returns the Builder of an operation whose input is a T: front-ends decode into
// a fresh *T, its schema is reflected from T's json tags (a field type may supply its
// own schema with a JSONSchema method), and run receives a copy of the decoded input.
func Func[T, E any](
	run func(ctx context.Context, in *T, env E, obs interfaces.ExecutionObserver) error,
) Builder[E] {
	return func(desc types.OperationDescriptor, env E) interfaces.Operation {
		return &funcOp[T, E]{
			desc: desc, env: env, run: run,
			schema: sync.OnceValue(func() []byte { return InputSchema(new(T)) }),
		}
	}
}

// Descriptor returns the operation's descriptor.
func (o *funcOp[T, E]) Descriptor() types.OperationDescriptor { return o.desc }

// NewInput returns a fresh *T for a front-end to decode arguments into.
func (o *funcOp[T, E]) NewInput() any { return new(T) }

// InputSchema returns T's JSON Schema.
func (o *funcOp[T, E]) InputSchema() []byte { return o.schema() }

// Run executes the operation over a copy of the input; an input that is not a non-nil
// *T is CodeInvalidInput naming the operation.
func (o *funcOp[T, E]) Run(
	ctx context.Context,
	input any,
	obs interfaces.ExecutionObserver,
) error {
	in, ok := input.(*T)
	if !ok || in == nil {
		var zero T
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: input is %T, want *%T", o.desc.Name, input, zero,
		))
	}
	cp := *in
	return o.run(ctx, &cp, o.env, obs)
}

// seededOp seeds the defaults an omitted argument takes into every fresh input.
type seededOp[T any] struct {
	interfaces.Operation

	// seed sets the input's defaults.
	seed func(in *T)
}

// WithDefaults wraps b so every input it hands out starts from seed's defaults, which a
// front-end's decode leaves unless the caller sets them.
func WithDefaults[T, E any](b Builder[E], seed func(in *T)) Builder[E] {
	return func(desc types.OperationDescriptor, env E) interfaces.Operation {
		return seededOp[T]{Operation: b(desc, env), seed: seed}
	}
}

// NewInput returns the wrapped operation's fresh *T with the defaults seeded.
func (o seededOp[T]) NewInput() any {
	in := o.Operation.NewInput()
	if t, ok := in.(*T); ok {
		o.seed(t)
	}
	return in
}

// InputSchema reflects v's json-tagged fields into an inlined, closed object schema
// with no $schema version (an MCP tool requires a top-level "type": "object"). Input
// types are static structs, so a marshal failure is a programming error and panics.
func InputSchema(v any) []byte {
	r := &jsonschema.Reflector{DoNotReference: true, ExpandedStruct: true}
	s := r.Reflect(v)
	s.Version = ""
	blob, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("ops: reflect schema of %s: %v", reflect.TypeOf(v), err))
	}
	return blob
}

// Decode decodes raw JSON arguments into op's fresh input, rejecting a field the input
// does not declare; empty raw leaves the input's defaults. A bad argument is
// CodeInvalidInput naming the operation.
func Decode(op interfaces.Operation, raw []byte) (any, error) {
	input := op.NewInput()
	if len(bytes.TrimSpace(raw)) == 0 {
		return input, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(input); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: decode arguments", op.Descriptor().Name,
		))
	}
	return input, nil
}
