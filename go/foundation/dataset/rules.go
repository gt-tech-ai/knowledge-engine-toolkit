package dataset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// ruleEnvBaseOptions sizes the CEL options every rule environment starts with.
const ruleEnvBaseOptions = 8

// Rule is one constraint over a dataset, as data: a CEL expression over its sections.
type Rule struct {
	// Name identifies the rule in a failure.
	Name string `yaml:"name" json:"name"`

	// Expr is the CEL expression. Each top-level section whose key is a CEL identifier is
	// a variable of that name, and data is every section by key. It evaluates to true
	// (pass), false (fail with Message), or a list of strings (fail once per string;
	// empty passes).
	Expr string `yaml:"expr" json:"expr"`

	// Message is the failure a false result reports.
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// dataVar is the CEL variable holding every section by key.
const dataVar = "data"

// Check evaluates every rule over d's sections and reports every failure in one
// CodeInvalidInput error, one line per failure prefixed by its rule's name. Every rule
// is compiled first, and only the sections the rules name are decoded (all of them when
// one names data). A rule that does not compile, fails to evaluate, or evaluates to
// neither a bool nor a list of strings is a failure of that rule. No rules, or all
// passing, is nil.
func (d *Dataset) Check(rules []Rule) error {
	if len(rules) == 0 {
		return nil
	}

	keys := d.keys()

	env, err := ruleEnv(keys)
	if err != nil {
		return err
	}

	progs, need, failures := compileRules(env, rules)

	if need[dataVar] {
		for _, k := range keys {
			need[k] = true
		}
	}

	act, err := d.activation(keys, need)
	if err != nil {
		return err
	}

	failures = append(failures, evalRules(rules, progs, act)...)

	if len(failures) == 0 {
		return nil
	}

	return apperr.New(apperr.CodeInvalidInput,
		"data rules failed:\n  - "+strings.Join(failures, "\n  - "))
}

// compileRules compiles each rule, returning a nil program and a failure for one that
// does not, along with the variables the compiled rules use.
func compileRules(
	env *cel.Env,
	rules []Rule,
) (programs []cel.Program, needed map[string]bool, problems []string) {
	progs := make([]cel.Program, len(rules))
	need := map[string]bool{}

	var failures []string

	for index := range rules {
		prg, uses, err := compileRule(env, rules[index].Expr)
		if err != nil {
			failures = append(
				failures,
				rules[index].Name+": does not compile: "+err.Error(),
			)

			continue
		}

		progs[index] = prg

		maps.Copy(need, uses)
	}

	return progs, need, failures
}

// evalRules runs each compiled rule, collecting its failure messages.
func evalRules(rules []Rule, progs []cel.Program, act map[string]any) []string {
	var failures []string

	for index := range rules {
		if progs[index] == nil {
			continue
		}

		for _, msg := range evalRule(progs[index], act, rules[index].Message) {
			failures = append(failures, rules[index].Name+": "+msg)
		}
	}

	return failures
}

// Validate checks every section of d, as one object keyed by section, against the JSON
// Schema document schema. Data the schema rejects, or a schema that does not compile, is
// CodeInvalidInput carrying the validator's report (each failing instance location).
func (d *Dataset) Validate(schema []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "parse data schema")
	}

	const url = "dataset.schema.json"

	compiler := jsonschema.NewCompiler()

	err = compiler.AddResource(url, doc)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "load data schema")
	}

	sch, err := compiler.Compile(url)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "compile data schema")
	}

	values, err := d.values()
	if err != nil {
		return err
	}

	raw, err := json.Marshal(values)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "encode data as JSON")
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "decode data JSON")
	}

	err = sch.Validate(inst)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "data does not match its schema")
	}

	return nil
}

// values decodes every section of d into plain Go values, by key.
func (d *Dataset) values() (map[string]any, error) {
	all := map[string]bool{}
	for _, k := range d.keys() {
		all[k] = true
	}

	return d.decoded(all)
}

// decoded decodes the sections named in need into plain Go values, by key.
func (d *Dataset) decoded(need map[string]bool) (map[string]any, error) {
	out := make(map[string]any, len(need))
	for _, key := range slices.Sorted(maps.Keys(need)) {
		nodeValue, ok, err := d.node(key)
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		var value any

		err = nodeValue.Decode(&value)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
				"decode section "+key+" from "+d.sourceOf(key))
		}

		out[key] = value
	}

	return out, nil
}

// activation is the CEL activation over the sections in need: each bound to its own
// variable when its key is a CEL identifier, and all of them under data.
func (d *Dataset) activation(
	keys []string,
	need map[string]bool,
) (map[string]any, error) {
	values, err := d.decoded(need)
	if err != nil {
		return nil, err
	}

	act := map[string]any{dataVar: values}
	for _, k := range ruleVars(keys) {
		if v, ok := values[k]; ok {
			act[k] = v
		}
	}

	return act, nil
}

// ruleVars returns the keys bound as CEL variables: the identifier-shaped ones other
// than data.
func ruleVars(keys []string) []string {
	ident := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	out := make([]string, 0, len(keys))

	for _, k := range keys {
		if k != dataVar && ident.MatchString(k) {
			out = append(out, k)
		}
	}

	return out
}

// ruleEnv builds the CEL environment over the section keys: the standard library plus
// optional types and the strings, lists, sets, bindings, math and regex extensions,
// with each identifier-shaped key and data declared.
func ruleEnv(keys []string) (*cel.Env, error) {
	vars := ruleVars(keys)
	opts := make([]cel.EnvOption, 0, ruleEnvBaseOptions+len(vars))

	opts = append(opts, ext.Strings(), ext.Lists(), ext.Sets(), cel.OptionalTypes(),
		ext.Bindings(), ext.Math(), ext.Regex(), cel.Variable(dataVar, cel.DynType))

	for _, k := range vars {
		opts = append(opts, cel.Variable(k, cel.DynType))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "cel environment")
	}

	return env, nil
}

// compileRule type-checks expr and returns its program and the variables it names.
func compileRule(env *cel.Env, expr string) (cel.Program, map[string]bool, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, nil, apperr.Wrap(iss.Err(), apperr.CodeInvalidInput, "compile")
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, nil, apperr.Wrap(err, apperr.CodeInvalidInput, "program")
	}

	checked, err := cel.AstToCheckedExpr(ast)
	if err != nil {
		return nil, nil, apperr.Wrap(err, apperr.CodeInternal, "references")
	}

	uses := map[string]bool{}

	for _, r := range checked.GetReferenceMap() {
		if name := r.GetName(); name != "" {
			uses[name] = true
		}
	}

	return prg, uses, nil
}

// evalRule evaluates prg over act, returning its failure messages (none when it passes).
func evalRule(prg cel.Program, act map[string]any, message string) []string {
	out, _, err := prg.Eval(act)
	if err != nil {
		return []string{"does not evaluate: " + err.Error()}
	}

	return ruleResult(out, message)
}

// ruleResult reads a rule's value: true passes, false fails with message, and a list of
// strings fails once per string. Any other value is a failure naming its type.
func ruleResult(val ref.Val, message string) []string {
	switch got := val.Value().(type) {
	case bool:
		if got {
			return nil
		}

		if message == "" {
			message = "evaluated to false"
		}

		return []string{message}
	default:
		native, err := val.ConvertToNative(reflect.TypeFor[[]string]())
		if err != nil {
			return []string{
				fmt.Sprintf(
					"evaluates to %s, want a bool or a list of strings",
					val.Type().TypeName(),
				),
			}
		}

		msgs := stdx.As[[]string](native)

		return msgs
	}
}
