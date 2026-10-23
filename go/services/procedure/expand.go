package procedure

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// placeholder matches `{name}` in step text; splat matches an argument that is exactly
// `{name...}`.
var (
	placeholder = regexp.MustCompile(`\{([A-Za-z_]\w*)\}`)
	splat       = regexp.MustCompile(`^\{([A-Za-z_]\w*)\.{3}\}$`)
)

// celLibs are the CEL extensions every procedure expression may use.
func celLibs() cel.EnvOption {
	return cel.Lib(libs{})
}

// libs bundles the CEL string, list and set extensions.
type libs struct{}

// CompileOptions returns the extensions.
func (libs) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{ext.Strings(), ext.Lists(), ext.Sets(), ext.Encoders()}
}

// ProgramOptions returns no program options.
func (libs) ProgramOptions() []cel.ProgramOption { return nil }

// exprArg reports whether arg is exactly `${expr}`, returning expr.
func exprArg(arg string) (string, bool) {
	if strings.HasPrefix(arg, "${") && strings.HasSuffix(arg, "}") {
		return arg[2 : len(arg)-1], true
	}
	return "", false
}

// eval evaluates the compiled src over vars as a plain Go value (JSON-shaped: numbers
// are float64).
func (c *compiled) eval(ctx context.Context, src string, vars Vars) (any, error) {
	out, _, err := c.progs[src].ContextEval(ctx, map[string]any(vars))
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, src)
	}
	pb, err := out.ConvertToNative(reflect.TypeFor[*structpb.Value]())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, src)
	}
	return pb.(*structpb.Value).AsInterface(), nil
}

// evalBool evaluates a condition.
func (c *compiled) evalBool(ctx context.Context, src string, vars Vars) (bool, error) {
	v, err := c.eval(ctx, src, vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, apperr.New(apperr.CodeInvalidInput, src+": not a bool")
	}
	return b, nil
}

// evalList evaluates a loop domain.
func (c *compiled) evalList(ctx context.Context, src string, vars Vars) ([]any, error) {
	v, err := c.eval(ctx, src, vars)
	if err != nil {
		return nil, err
	}
	l, ok := v.([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput, src+": not a list")
	}
	return l, nil
}

// text expands every `{name}` in s; a variable with no value is CodeInvalidInput.
func text(s string, vars Vars) (string, error) {
	var missing string
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := vars[name]
		if !ok {
			missing = name
			return m
		}
		return scalar(v)
	})
	if missing != "" {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("no variable %q", missing),
		)
	}
	return out, nil
}

// texts expands each of ss.
func texts(ss []string, vars Vars) ([]string, error) {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		t, err := text(s, vars)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// args expands a tool step's arguments: `{name...}` and a list-valued `${expr}` splat.
func (c *compiled) args(ctx context.Context, in []string, vars Vars) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, a := range in {
		var v any
		switch m := splat.FindStringSubmatch(a); {
		case m != nil:
			var ok bool
			if v, ok = vars[m[1]]; !ok {
				return nil, apperr.New(
					apperr.CodeInvalidInput,
					fmt.Sprintf("no variable %q", m[1]),
				)
			}
		default:
			x, isExpr := exprArg(a)
			if !isExpr {
				t, err := text(a, vars)
				if err != nil {
					return nil, err
				}
				out = append(out, t)
				continue
			}
			var err error
			if v, err = c.eval(ctx, x, vars); err != nil {
				return nil, err
			}
		}
		out = append(out, list(v)...)
	}
	return out, nil
}

// list renders v as arguments: each element of a list, else v alone.
func list(v any) []string {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return []string{scalar(v)}
	}
	out := make([]string, rv.Len())
	for i := range out {
		out[i] = scalar(rv.Index(i).Interface())
	}
	return out
}

// scalar renders one value as text.
func scalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
