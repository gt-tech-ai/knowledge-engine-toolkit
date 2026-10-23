package ops

import (
	"context"
	"encoding/json"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// MapRun runs an operation over its map input.
type MapRun[E any] func(
	ctx context.Context,
	in map[string]any,
	env E,
	obs interfaces.ExecutionObserver,
) error

// JSON Schema keywords and types the map schema uses.
const (
	schemaType   = "type"
	schemaString = "string"
)

// mapOp is an operation whose input is a map shaped by flag specs.
type mapOp[E any] struct {
	// env is the captured environment.
	env E

	// run executes the operation.
	run MapRun[E]

	// desc is the operation's descriptor.
	desc types.OperationDescriptor

	// schema is the input's JSON Schema.
	schema []byte
}

// MapInput returns the Builder of an operation whose input is a map[string]any keyed by
// the flags' fields and the arguments' names, so a command defined as data (a
// CommandSpec) needs no Go input type; its schema is derived from the same specs, so the
// MCP tool of the operation takes exactly the command's flags.
func MapInput[E any](
	flags []types.FlagSpec,
	args types.ArgSpec,
	run MapRun[E],
) Builder[E] {
	schema := MapSchema(flags, args)
	return func(desc types.OperationDescriptor, env E) interfaces.Operation {
		return &mapOp[E]{desc: desc, env: env, run: run, schema: schema}
	}
}

// Descriptor returns the operation's descriptor.
func (o *mapOp[E]) Descriptor() types.OperationDescriptor { return o.desc }

// NewInput returns a fresh empty input.
func (o *mapOp[E]) NewInput() any { return &map[string]any{} }

// InputSchema returns the schema derived from the flags.
func (o *mapOp[E]) InputSchema() []byte { return o.schema }

// Run executes the operation; an input that is not a *map[string]any is
// CodeInvalidInput.
func (o *mapOp[E]) Run(
	ctx context.Context,
	input any,
	obs interfaces.ExecutionObserver,
) error {
	in, ok := input.(*map[string]any)
	if !ok || in == nil {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: input is %T, want *map[string]any", o.desc.Name, input))
	}
	cp := make(map[string]any, len(*in))
	for k, v := range *in {
		cp[k] = v
	}
	return o.run(ctx, cp, o.env, obs)
}

// MapSchema returns the closed object schema of a map input: one property per flag
// (by its field) and per argument, required flags required.
func MapSchema(flags []types.FlagSpec, args types.ArgSpec) []byte {
	props := map[string]any{}
	var required []string
	for i := range flags {
		f := &flags[i]
		p := map[string]any{schemaType: jsonType(f.Type)}
		if f.Type == types.FlagStrings {
			p["items"] = map[string]any{schemaType: schemaString}
		}
		if f.Usage != "" {
			p["description"] = f.Usage
		}
		props[f.FlagField()] = p
		if f.Required {
			required = append(required, f.FlagField())
		}
	}
	for i, n := range args.Names {
		props[n] = map[string]any{schemaType: schemaString}
		if i < args.Min {
			required = append(required, n)
		}
	}
	if args.Rest != "" {
		props[args.Rest] = map[string]any{
			schemaType: "array",
			"items":    map[string]any{schemaType: schemaString},
		}
	}
	s := map[string]any{
		schemaType:             "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	blob, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("ops: map schema: %v", err))
	}
	return blob
}

// jsonType is the JSON Schema type of a flag type.
func jsonType(t string) string {
	switch t {
	case types.FlagString:
		return schemaString
	case types.FlagInt, types.FlagDuration:
		return "integer"
	case types.FlagStrings:
		return "array"
	default:
		return "boolean"
	}
}
