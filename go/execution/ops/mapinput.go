package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

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

	// flags shape the input; an omitted flag runs as its default.
	flags []types.FlagSpec

	// args are the input's arguments; an omitted one runs as "" (the rest as no items).
	args types.ArgSpec
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
		return &mapOp[E]{
			desc: desc, env: env, run: run, schema: schema, flags: flags, args: args,
		}
	}
}

// Descriptor returns the operation's descriptor.
func (o *mapOp[E]) Descriptor() types.OperationDescriptor { return o.desc }

// NewInput returns a fresh empty input.
func (o *mapOp[E]) NewInput() any { return &map[string]any{} }

// InputSchema returns the schema derived from the flags.
func (o *mapOp[E]) InputSchema() []byte { return o.schema }

// undeclared returns the first key of in, in sorted order, that no argument and no flag
// declares — a secret flag counting only when local — or "".
func (o *mapOp[E]) undeclared(in map[string]any, local bool) string {
	for _, k := range slices.Sorted(maps.Keys(in)) {
		if !o.declares(k, local) {
			return k
		}
	}
	return ""
}

// declares reports whether key names one of the op's arguments, or a flag (a secret one
// only when local).
func (o *mapOp[E]) declares(key string, local bool) bool {
	if key == o.args.Rest && key != "" || slices.Contains(o.args.Names, key) {
		return true
	}
	for i := range o.flags {
		if o.flags[i].FlagField() == key {
			return local || !o.flags[i].Secret
		}
	}
	return false
}

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
	FillInput(o.flags, o.args, cp)
	return o.run(ctx, cp, o.env, obs)
}

// FillInput puts every key the caller omitted into in — a flag's default, else its type's
// zero; "" for an argument and no items for the rest — and coerces a sent flag to its
// type (JSON decodes every number as a float64, every list as []any), so a run reads
// every input the command declares, typed the same whichever front-end sent it. A
// composite that runs an operation's procedure directly, outside its MapInput operation,
// fills the same way so the procedure sees every declared input.
func FillInput(flags []types.FlagSpec, args types.ArgSpec, in map[string]any) {
	for i := range flags {
		f := &flags[i]
		v, ok := in[f.FlagField()]
		if !ok {
			in[f.FlagField()] = flagDefault(f)
			continue
		}
		in[f.FlagField()] = coerce(f.Type, v)
	}
	for _, n := range args.Names {
		if _, ok := in[n]; !ok {
			in[n] = ""
		}
	}
	if args.Rest != "" {
		if _, ok := in[args.Rest]; !ok {
			in[args.Rest] = []string{}
		}
	}
}

// coerce converts a JSON-decoded value to the Go type the flag's type binds (int,
// int64 nanoseconds, []string); any other value is returned as sent.
func coerce(flagType string, v any) any {
	switch x := v.(type) {
	case float64:
		switch flagType {
		case types.FlagInt:
			return int(x)
		case types.FlagDuration:
			return int64(x)
		}
	case []any:
		if flagType == types.FlagStrings {
			out := make([]string, 0, len(x))
			for _, item := range x {
				out = append(out, fmt.Sprint(item))
			}
			return out
		}
	}
	return v
}

// flagDefault is f's declared default, else the zero of its type.
func flagDefault(f *types.FlagSpec) any {
	if f.Default != nil {
		return f.Default
	}
	switch f.Type {
	case types.FlagString:
		return ""
	case types.FlagInt:
		return 0
	case types.FlagStrings:
		return []string{}
	case types.FlagDuration:
		return int64(0)
	default:
		return false
	}
}

// MapSchema returns the closed object schema of a map input: one property per flag
// (by its field) and per argument, required flags required. A secret flag has no
// property, so a schema-driven caller (an MCP tool) never asks for it.
func MapSchema(flags []types.FlagSpec, args types.ArgSpec) []byte {
	props := map[string]any{}
	var required []string
	for i := range flags {
		f := &flags[i]
		if f.Secret {
			continue
		}
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
