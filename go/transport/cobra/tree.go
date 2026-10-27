package cobra

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	spf "github.com/spf13/cobra"
	"github.com/spf13/pflag"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// Tree returns the command tree root describes. A group (no Op) prints its help; a leaf
// runs its operation. A leaf with no flags or arguments in its spec binds the
// operation's input struct tags as Command does (ancestors' persistent flags are then
// the root's to consume, in a PersistentPreRun); otherwise its flags (its own and its
// ancestors' persistent ones) and positional arguments become a JSON object keyed by
// each flag's field and argument name, decoded into the operation's input (a struct by
// its json tags, or an ops.MapInput map) — a flag counts when set or when its spec has a
// default. Subcommands are listed under their Group, in first-appearance order. An
// unknown operation or a bad flag type is an error naming the command. With
// opts.Resolve set, reg may be nil: each leaf's flags come from its spec alone, and its
// operation is found in the registry Resolve returns when it runs, so an unknown one
// fails that run. Every leaf carries its operation under OperationAnnotation.
func Tree(root types.CommandSpec, reg *ops.Registry, opts Options) (*spf.Command, error) {
	return node(&root, nil, reg, opts)
}

// node builds spec's command; inherited are the persistent flags of its ancestors.
func node(
	spec *types.CommandSpec,
	inherited []types.FlagSpec,
	reg *ops.Registry,
	opts Options,
) (*spf.Command, error) {
	cmd, err := leafOrGroup(spec, inherited, reg, opts)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Code(err), "command "+spec.Use)
	}
	cmd.Use, cmd.Short, cmd.Long, cmd.Example = spec.Use, spec.Short, spec.Long, spec.Example
	cmd.Aliases, cmd.Hidden, cmd.SilenceUsage = spec.Aliases, spec.Hidden, true
	for i := range spec.Flags {
		f := &spec.Flags[i]
		fs := cmd.Flags()
		if f.Persistent {
			fs = cmd.PersistentFlags()
		}
		if err := addSpecFlag(fs, f); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "command "+spec.Use)
		}
		if f.Required {
			_ = cmd.MarkFlagRequired(f.Name) // the flag was just added
		}
	}
	scope := slices.Concat(inherited, persistent(spec.Flags))
	var groups []string
	for i := range spec.Commands {
		sub := &spec.Commands[i]
		child, err := node(sub, scope, reg, opts)
		if err != nil {
			return nil, err
		}
		if sub.Group != "" && !slices.Contains(groups, sub.Group) {
			groups = append(groups, sub.Group)
			cmd.AddGroup(&spf.Group{ID: sub.Group, Title: sub.Group + ":"})
		}
		child.GroupID = sub.Group
		cmd.AddCommand(child)
	}
	return cmd, nil
}

// leafOrGroup returns spec's command with its run bound: a group's run prints help.
func leafOrGroup(
	spec *types.CommandSpec,
	inherited []types.FlagSpec,
	reg *ops.Registry,
	opts Options,
) (*spf.Command, error) {
	if spec.Op == "" {
		return &spf.Command{
			Args: spf.NoArgs,
			RunE: func(c *spf.Command, _ []string) error { return c.Help() },
		}, nil
	}
	bare := len(spec.Flags) == 0 && len(spec.Args.Names) == 0 && spec.Args.Rest == ""
	if bare && opts.Resolve == nil {
		return Command(reg, spec.Op, opts)
	}
	if opts.Resolve == nil {
		if _, err := reg.Find(spec.Op); err != nil {
			return nil, err
		}
	}
	scope := slices.Concat(inherited, spec.Flags)
	cmd := &spf.Command{
		Args:        argsRule(spec.Args),
		Annotations: map[string]string{OperationAnnotation: spec.Op},
	}
	cmd.RunE = func(c *spf.Command, args []string) error {
		runReg, obs := reg, opts.Observer
		if opts.Resolve != nil {
			var err error
			if runReg, obs, err = opts.Resolve(c.Context(), spec.Op); err != nil {
				return err
			}
		}
		op, err := runReg.Find(spec.Op)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(values(c.Flags(), scope, spec.Args, args))
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "encode input")
		}
		// A human typed these flags, so the op's secret flags are admitted.
		input, err := ops.DecodeLocal(op, raw)
		if err != nil {
			return err
		}
		return runReg.Run(c.Context(), spec.Op, input, obs)
	}
	return cmd, nil
}

// values collects the set or defaulted flags in scope and the positional arguments.
func values(
	fs *pflag.FlagSet,
	scope []types.FlagSpec,
	spec types.ArgSpec,
	args []string,
) map[string]any {
	out := map[string]any{}
	for i := range scope {
		f := &scope[i]
		if !fs.Changed(f.Name) && f.Default == nil {
			continue
		}
		out[f.FlagField()] = flagValue(fs, f)
	}
	for i, a := range args {
		if i < len(spec.Names) {
			out[spec.Names[i]] = a
			continue
		}
		out[spec.Rest] = args[i:]
		break
	}
	return out
}

// flagValue reads f's parsed value (a duration as nanoseconds).
func flagValue(fs *pflag.FlagSet, f *types.FlagSpec) any {
	var v any
	switch f.Type {
	case types.FlagString:
		v, _ = fs.GetString(f.Name)
	case types.FlagInt:
		v, _ = fs.GetInt(f.Name)
	case types.FlagStrings:
		v, _ = fs.GetStringSlice(f.Name)
	case types.FlagStringArray:
		v, _ = fs.GetStringArray(f.Name)
	case types.FlagDuration:
		d, _ := fs.GetDuration(f.Name)
		v = d.Nanoseconds()
	default:
		v, _ = fs.GetBool(f.Name)
	}
	return v
}

// addSpecFlag registers f on fs with its default.
func addSpecFlag(fs *pflag.FlagSet, f *types.FlagSpec) error {
	if len(f.Short) > 1 {
		return apperr.New(apperr.CodeInvalidInput, "flag "+f.Name+": short is one letter")
	}
	def := f.Default
	switch f.Type {
	case types.FlagString:
		s, _ := def.(string)
		fs.StringP(f.Name, f.Short, s, f.Usage)
	case types.FlagInt:
		n, _ := def.(int)
		fs.IntP(f.Name, f.Short, n, f.Usage)
	case types.FlagStrings:
		fs.StringSliceP(f.Name, f.Short, strs(def), f.Usage)
	case types.FlagStringArray:
		fs.StringArrayP(f.Name, f.Short, strs(def), f.Usage)
	case types.FlagDuration:
		fs.DurationP(f.Name, f.Short, 0, f.Usage)
		if s, ok := def.(string); ok {
			if err := fs.Set(f.Name, s); err != nil {
				return apperr.Wrap(
					err,
					apperr.CodeInvalidInput,
					"flag "+f.Name+" default",
				)
			}
		}
	case "", types.FlagBool:
		b, _ := def.(bool)
		fs.BoolP(f.Name, f.Short, b, f.Usage)
	default:
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"flag %s: unknown type %q", f.Name, f.Type))
	}
	if f.Hidden {
		_ = fs.MarkHidden(f.Name) // the flag was just added
	}
	return nil
}

// argsRule is the positional-argument rule of spec.
func argsRule(spec types.ArgSpec) spf.PositionalArgs {
	if spec.Rest != "" {
		return spf.MinimumNArgs(spec.Min)
	}
	return spf.RangeArgs(spec.Min, len(spec.Names))
}

// persistent returns the persistent flags of fs.
func persistent(fs []types.FlagSpec) []types.FlagSpec {
	var out []types.FlagSpec
	for i := range fs {
		if fs[i].Persistent {
			out = append(out, fs[i])
		}
	}
	return out
}

// strs converts a decoded YAML list default to strings.
func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = strings.TrimSpace(fmt.Sprint(e))
		}
		return out
	}
	return nil
}
