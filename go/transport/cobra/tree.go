package cobra

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	spf "github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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
// fails that run. With opts.Before or opts.After set every leaf's flags likewise come
// from its spec, and the hooks wrap each run. Every leaf carries its operation under
// OperationAnnotation.
// root must not be nil.
func Tree(
	rootRef *types.CommandSpec,
	reg *ops.Registry,
	opts Options,
) (*spf.Command, error) {
	root := *rootRef

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

	err = addCommandFlags(cmd, spec)
	if err != nil {
		return nil, err
	}

	scope := slices.Concat(inherited, persistent(spec.Flags))

	err = addSubcommands(cmd, spec, scope, reg, opts)
	if err != nil {
		return nil, err
	}

	return cmd, nil
}

// addCommandFlags registers the spec's flags on the command.
func addCommandFlags(cmd *spf.Command, spec *types.CommandSpec) error {
	for i := range spec.Flags {
		flagSpec := &spec.Flags[i]
		flagSet := cmd.Flags()

		if flagSpec.Persistent {
			flagSet = cmd.PersistentFlags()
		}

		err := addSpecFlag(flagSet, flagSpec)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "command "+spec.Use)
		}

		if flagSpec.Required {
			_ = cmd.MarkFlagRequired(flagSpec.Name) // the flag was just added
		}
	}

	return nil
}

// addSubcommands builds the children, declaring each help group once.
func addSubcommands(
	cmd *spf.Command,
	spec *types.CommandSpec,
	scope []types.FlagSpec,
	reg *ops.Registry,
	opts Options,
) error {
	var groups []string

	for i := range spec.Commands {
		sub := &spec.Commands[i]

		child, err := node(sub, scope, reg, opts)
		if err != nil {
			return err
		}

		if sub.Group != "" && !slices.Contains(groups, sub.Group) {
			groups = append(groups, sub.Group)
			cmd.AddGroup(&spf.Group{ID: sub.Group, Title: sub.Group + ":"})
		}

		child.GroupID = sub.Group
		cmd.AddCommand(child)
	}

	return nil
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

	if isBareSpec(spec) && !hasHooks(&opts) {
		return Command(reg, spec.Op, opts)
	}

	if opts.Resolve == nil {
		_, err := reg.Find(spec.Op)
		if err != nil {
			return nil, err
		}
	}

	return &spf.Command{
		Args:        argsRule(spec.Args),
		Annotations: map[string]string{OperationAnnotation: spec.Op},
		RunE:        hookedRun(spec, slices.Concat(inherited, spec.Flags), reg, opts),
	}, nil
}

// isBareSpec reports a command that declares no flags or arguments.
func isBareSpec(spec *types.CommandSpec) bool {
	return len(spec.Flags) == 0 && len(spec.Args.Names) == 0 && spec.Args.Rest == ""
}

// hasHooks reports whether any resolve, before or after hook is set.
func hasHooks(opts *Options) bool {
	return opts.Resolve != nil || opts.Before != nil || opts.After != nil
}

// hookedRun runs the operation between the Before and After hooks.
func hookedRun(
	spec *types.CommandSpec, scope []types.FlagSpec, reg *ops.Registry, opts Options,
) func(*spf.Command, []string) error {
	return func(command *spf.Command, args []string) error {
		lookup := values(command.Flags(), scope, spec.Args, args)
		name := spec.Op

		var err error

		if opts.Before != nil {
			name, err = opts.Before(command, name, lookup)
		}

		if err == nil {
			err = run(command, name, lookup, reg, opts)
		}

		if opts.After != nil {
			return opts.After(command, name, err)
		}

		return err
	}
}

// run decodes in into operation name's input and runs it, in the registry and under the
// observer opts.Resolve returns for the run (else reg and opts.Observer).
func run(
	command *spf.Command,
	name string,
	lookup map[string]any,
	reg *ops.Registry,
	opts Options,
) error {
	runReg, obs := reg, opts.Observer
	if opts.Resolve != nil {
		var err error

		runReg, obs, err = opts.Resolve(command.Context(), name)
		if err != nil {
			return err
		}
	}

	raw, err := json.Marshal(lookup)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "encode input")
	}

	if opts.Run != nil {
		return opts.Run(command.Context(), runReg, name, raw, obs)
	}

	return RunLocal(command.Context(), runReg, name, raw, obs)
}

// RunLocal runs the operation named name in reg with raw JSON input a human typed at
// the terminal, so the operation's secret flags are admitted: a Tree leaf's default
// run, and the local half of an Options.Run that also schedules.
func RunLocal(
	ctx context.Context, reg *ops.Registry, name string, raw []byte,
	obs interfaces.ExecutionObserver,
) error {
	op, err := reg.Find(name)
	if err != nil {
		return err
	}

	input, err := ops.DecodeLocal(op, raw)
	if err != nil {
		return err
	}

	return reg.Run(ctx, name, input, obs)
}

// values collects the set or defaulted flags in scope and the positional arguments.
func values(
	flagSet *pflag.FlagSet,
	scope []types.FlagSpec,
	spec types.ArgSpec,
	args []string,
) map[string]any {
	out := map[string]any{}

	for i := range scope {
		f := &scope[i]
		if !flagSet.Changed(f.Name) && f.Default == nil {
			continue
		}

		out[f.FlagField()] = flagValue(flagSet, f)
	}

	for index, a := range args {
		if index < len(spec.Names) {
			out[spec.Names[index]] = a

			continue
		}

		out[spec.Rest] = args[index:]

		break
	}

	return out
}

// flagValue reads f's parsed value (a duration as nanoseconds).
func flagValue(flagSet *pflag.FlagSet, flagSpec *types.FlagSpec) any {
	var value any

	switch flagSpec.Type {
	case types.FlagString:
		value, _ = flagSet.GetString(flagSpec.Name)
	case types.FlagInt:
		value, _ = flagSet.GetInt(flagSpec.Name)
	case types.FlagStrings:
		value, _ = flagSet.GetStringSlice(flagSpec.Name)
	case types.FlagStringArray:
		value, _ = flagSet.GetStringArray(flagSpec.Name)
	case types.FlagDuration:
		d, _ := flagSet.GetDuration(flagSpec.Name)

		value = d.Nanoseconds()
	default:
		value, _ = flagSet.GetBool(flagSpec.Name)
	}

	return value
}

// addSpecFlag registers f on fs with its default.
func addSpecFlag(flagSet *pflag.FlagSet, flagSpec *types.FlagSpec) error {
	if len(flagSpec.Short) > 1 {
		return apperr.New(
			apperr.CodeInvalidInput,
			"flag "+flagSpec.Name+": short is one letter",
		)
	}

	err := defineSpecFlag(flagSet, flagSpec)
	if err != nil {
		return err
	}

	if flagSpec.Hidden {
		_ = flagSet.MarkHidden(flagSpec.Name) // the flag was just added
	}

	return nil
}

// defineSpecFlag adds the flag by its declared type; an empty type is a bool.
func defineSpecFlag(flagSet *pflag.FlagSet, flagSpec *types.FlagSpec) error {
	def := flagSpec.Default

	switch flagSpec.Type {
	case types.FlagString:
		s := stdx.As[string](def)
		flagSet.StringP(flagSpec.Name, flagSpec.Short, s, flagSpec.Usage)
	case types.FlagInt:
		n := stdx.As[int](def)
		flagSet.IntP(flagSpec.Name, flagSpec.Short, n, flagSpec.Usage)
	case types.FlagStrings:
		flagSet.StringSliceP(flagSpec.Name, flagSpec.Short, strs(def), flagSpec.Usage)
	case types.FlagStringArray:
		flagSet.StringArrayP(flagSpec.Name, flagSpec.Short, strs(def), flagSpec.Usage)
	case types.FlagDuration:
		d, err := durationDefault(flagSpec)
		if err != nil {
			return err
		}

		flagSet.DurationP(flagSpec.Name, flagSpec.Short, d, flagSpec.Usage)
	case "", types.FlagBool:
		b := stdx.As[bool](def)
		flagSet.BoolP(flagSpec.Name, flagSpec.Short, b, flagSpec.Usage)
	default:
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"flag %s: unknown type %q", flagSpec.Name, flagSpec.Type))
	}

	return nil
}

// durationDefault parses a duration flag's default; a non-string default is zero.
func durationDefault(flagSpec *types.FlagSpec) (time.Duration, error) {
	s, ok := flagSpec.Default.(string)
	if !ok {
		return 0, nil
	}

	duration, err := time.ParseDuration(s)
	if err != nil {
		return 0, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"flag "+flagSpec.Name+" default",
		)
	}

	return duration, nil
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
