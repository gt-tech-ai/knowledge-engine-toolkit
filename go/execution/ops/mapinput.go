package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
func (*mapOp[E]) NewInput() any { return &map[string]any{} }

// InputSchema returns the schema derived from the flags.
func (o *mapOp[E]) InputSchema() []byte { return o.schema }

// flagAccess says which of an operation's flags a caller may set.
type flagAccess int

const (
	// publicFlags admits every flag except the secret ones (a remote caller).
	publicFlags flagAccess = iota
	// allFlags admits the secret flags too (a local caller).
	allFlags
)

// undeclared returns the first key of in, in sorted order, that no argument and no flag
// declares — a secret flag counting only when local — or "".
func (o *mapOp[E]) undeclared(in map[string]any, access flagAccess) string {
	for _, k := range slices.Sorted(maps.Keys(in)) {
		if !o.declares(k, access) {
			return k
		}
	}

	return ""
}

// declares reports whether key names one of the op's arguments, or a flag (a secret one
// only when local).
func (o *mapOp[E]) declares(key string, access flagAccess) bool {
	if key == o.args.Rest && key != "" || slices.Contains(o.args.Names, key) {
		return true
	}

	for i := range o.flags {
		if o.flags[i].FlagField() == key {
			return access == allFlags || !o.flags[i].Secret
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
	lookup, ok := input.(*map[string]any)
	if !ok || lookup == nil {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: input is %T, want *map[string]any", o.desc.Name, input))
	}

	cp := make(map[string]any, len(*lookup))
	maps.Copy(cp, *lookup)

	FillInput(o.flags, o.args, cp)

	return o.run(ctx, cp, o.env, obs)
}

// FillInput puts every key the caller omitted into in — a flag's default, else its type's
// zero; "" for an argument and no items for the rest — and coerces a sent flag to its
// type (JSON decodes every number as a float64, every list as []any), so a run reads
// every input the command declares, typed the same whichever front-end sent it. A
// composite that runs an operation's procedure directly, outside its MapInput operation,
// fills the same way so the procedure sees every declared input.
func FillInput(flags []types.FlagSpec, args types.ArgSpec, lookup map[string]any) {
	for i := range flags {
		fillFlag(&flags[i], lookup)
	}

	for _, n := range args.Names {
		setIfAbsent(lookup, n, "")
	}

	if args.Rest != "" {
		setIfAbsent(lookup, args.Rest, []string{})
	}
}

// fillFlag coerces a supplied flag, or sets its default.
func fillFlag(flagSpec *types.FlagSpec, lookup map[string]any) {
	value, ok := lookup[flagSpec.FlagField()]
	if !ok {
		lookup[flagSpec.FlagField()] = flagDefault(flagSpec)

		return
	}

	lookup[flagSpec.FlagField()] = coerce(flagSpec.Type, value)
}

// setIfAbsent sets key only when in has no value for it.
func setIfAbsent(in map[string]any, key string, value any) {
	if _, ok := in[key]; !ok {
		in[key] = value
	}
}

// coerce converts a JSON-decoded value to the Go type the flag's type binds (int, a
// time.Duration from nanoseconds or its text, []string); any other value is returned as
// sent.
func coerce(flagType string, value any) any {
	if flagType == types.FlagDuration {
		return duration(value)
	}

	switch amount := value.(type) {
	case float64:
		if flagType == types.FlagInt {
			return int(amount)
		}
	case []any:
		if types.IsListFlag(flagType) {
			return stringItems(amount)
		}
	default:
		// No other value changes the outcome.
	}

	return value
}

// stringItems renders each list item as a string.
func stringItems(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}

	return out
}

// flagDefault is f's declared default, else the zero of its type.
func flagDefault(f *types.FlagSpec) any {
	if f.Default != nil {
		return coerce(f.Type, f.Default)
	}

	switch f.Type {
	case types.FlagString:
		return ""
	case types.FlagInt:
		return 0
	case types.FlagStrings, types.FlagStringArray:
		return []string{}
	case types.FlagDuration:
		return time.Duration(0)
	default:
		return false
	}
}

// duration is v as a time.Duration: a number is nanoseconds, a string its text ("2m");
// any other value, or unparseable text, is returned as sent.
func duration(value any) any {
	switch count := value.(type) {
	case float64:
		return time.Duration(count)
	case int64:
		return time.Duration(count)
	case int:
		return time.Duration(count)
	case string:
		d, err := time.ParseDuration(count)
		if err == nil {
			return d
		}
	default:
		// No other value changes the outcome.
	}

	return value
}

// MapSchema returns the closed object schema of a map input: one property per flag
// (by its field) and per argument, required flags required. A secret flag has no
// property, so a schema-driven caller (an MCP tool) never asks for it.
func MapSchema(flags []types.FlagSpec, args types.ArgSpec) []byte {
	props := map[string]any{}

	required := flagProperties(flags, props)
	required = append(required, argProperties(args, props)...)

	lookup := map[string]any{
		schemaType:             "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		lookup["required"] = required
	}

	blob, err := json.Marshal(lookup)
	if err != nil {
		panic(fmt.Sprintf("ops: map schema: %v", err))
	}

	return blob
}

// flagProperties adds each non-secret flag to props and returns the required ones.
func flagProperties(flags []types.FlagSpec, props map[string]any) []string {
	var required []string

	for i := range flags {
		flagSpec := &flags[i]
		if flagSpec.Secret {
			continue
		}

		props[flagSpec.FlagField()] = flagProperty(flagSpec)

		if flagSpec.Required {
			required = append(required, flagSpec.FlagField())
		}
	}

	return required
}

// flagProperty is one flag's JSON schema.
func flagProperty(flagSpec *types.FlagSpec) map[string]any {
	lookup := map[string]any{schemaType: jsonType(flagSpec.Type)}
	if types.IsListFlag(flagSpec.Type) {
		lookup["items"] = map[string]any{schemaType: schemaString}
	}

	if flagSpec.Usage != "" {
		lookup["description"] = flagSpec.Usage
	}

	return lookup
}

// argProperties adds the positional arguments to props and returns the required ones.
func argProperties(args types.ArgSpec, props map[string]any) []string {
	var required []string

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

	return required
}

// jsonType is the JSON Schema type of a flag type.
func jsonType(t string) string {
	switch t {
	case types.FlagString:
		return schemaString
	case types.FlagInt, types.FlagDuration:
		return "integer"
	case types.FlagStrings, types.FlagStringArray:
		return "array"
	default:
		return "boolean"
	}
}
