// Package cobra is the command-line front-end of an operations registry
// (execution/ops): Command builds a Cobra command for one registered operation, its
// flags and positional arguments derived from the operation's input type, so a CLI and
// the other front-ends run the operation on the same input.
package cobra

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	spf "github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// flagTagFields is the comma-separated fields of a flag tag.
const flagTagFields = 3

// OperationAnnotation is the Cobra annotation key under which a Tree leaf names the
// operation it runs.
const OperationAnnotation = "operation"

// Options shapes a command.
type Options struct {
	// Observer receives the run's progress; nil reports nothing.
	Observer interfaces.ExecutionObserver

	// Resolve, when set, returns the registry and observer one run of the operation op
	// uses, from the run's context, in place of Tree's registry and Observer: for a CLI
	// that builds its registry after parsing (in a PersistentPreRun).
	Resolve func(ctx context.Context, op string) (
		*ops.Registry, interfaces.ExecutionObserver, error,
	)

	// Before, when set, runs before each Tree leaf's operation with the command, the
	// operation it names and the input object its flags and arguments built: it may change
	// the input (a value from stdin or the console) and returns the operation to run (op,
	// or a sibling the input selects, such as a --fix's mutating twin).
	Before func(c *spf.Command, op string, in map[string]any) (string, error)

	// After, when set, runs after each Tree leaf's run, failed or not, with the operation
	// that ran and its error; it returns the error the command reports.
	After func(c *spf.Command, op string, err error) error

	// Run, when set, runs each Tree leaf's operation in place of RunLocal, with the run's
	// registry, the operation and its input as JSON: a scheduled front-end passes
	// transport/cron.Run, so the same tree runs a leaf unattended (no secret flag, no
	// interactive operation).
	Run func(
		ctx context.Context, reg *ops.Registry, op string, raw []byte,
		obs interfaces.ExecutionObserver,
	) error

	// Use overrides the command's usage line; empty uses the operation's name.
	Use string
}

// Command returns the command that runs the operation named name. Each input field
// tagged `flag:"name,short,usage"` is a flag (bool, string, int, time.Duration or
// []string; a field also tagged jsonschema:"required" is a required flag), and each
// field tagged `arg:"n"` takes the n-th positional argument (a []string field takes it
// and the rest). Each run decodes into a fresh, default-seeded input: the flags Cobra
// parsed as set on the command line, then that run's positional arguments. Cobra keeps a
// flag's parsed state (its value and whether it was set) across executions of one
// command, so build a command per execution, as a CLI does once per process. An unknown
// or excluded operation fails like ops.Registry.Find, the command carries its operation
// under OperationAnnotation, and an unsupported field type or a
// malformed tag is CodeInvalidInput naming the field.
func Command(reg *ops.Registry, name string, opts Options) (*spf.Command, error) {
	operation, err := reg.Find(name)
	if err != nil {
		return nil, err
	}

	desc := operation.Descriptor()
	use := opts.Use

	if use == "" {
		use = desc.Name
	}

	cmd := &spf.Command{
		Use: use, Short: desc.Summary, SilenceUsage: true,
		Annotations: map[string]string{OperationAnnotation: name},
	}
	flags := operation.NewInput()

	secondBinding, err := bind(cmd, flags)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
			fmt.Sprintf("operation %q", name))
	}

	cmd.Args = spf.RangeArgs(0, secondBinding.maxArgs())
	cmd.RunE = func(command *spf.Command, args []string) error {
		input := operation.NewInput()
		dst := reflect.ValueOf(input).Elem()
		secondBinding.setFlags(command, reflect.ValueOf(flags).Elem(), dst)

		err := secondBinding.positional(dst, args)
		if err != nil {
			return err
		}

		return reg.Run(command.Context(), name, input, opts.Observer)
	}

	return cmd, nil
}

// binding is where a command's flags and positional arguments go. Cobra parses the
// flags into one input the command keeps; each run copies the flags Cobra records as
// set, and stores its own positional arguments, into a fresh input.
type binding struct {
	// args maps each positional index to its field's index in the input struct.
	args map[int]int

	// flags maps each flag name to its field's index in the input struct.
	flags map[string]int

	// rest is the index whose []string field takes the remaining arguments; -1 when
	// none does.
	rest int
}

// maxArgs is how many positional arguments the command accepts.
func (b *binding) maxArgs() int {
	if b.rest >= 0 {
		return int(^uint(0) >> 1)
	}

	return len(b.args)
}

// setFlags copies into dst each flag field Cobra records as set on c (src holds the
// parsed values); any other flag keeps dst's seeded default.
func (b *binding) setFlags(c *spf.Command, src, dst reflect.Value) {
	for name, i := range b.flags {
		if c.Flags().Changed(name) {
			dst.Field(i).Set(src.Field(i))
		}
	}
}

// positional stores args into their fields of dst.
func (b *binding) positional(dst reflect.Value, args []string) error {
	for index, text := range args {
		if b.rest >= 0 && index >= b.rest {
			f := dst.Field(b.args[b.rest])
			f.Set(reflect.Append(f, reflect.ValueOf(text)))

			continue
		}

		count, ok := b.args[index]
		if !ok {
			return apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("unexpected argument %q", text))
		}

		dst.Field(count).SetString(text)
	}

	return nil
}

// bind registers a flag or positional slot for each tagged field of *input.
func bind(cmd *spf.Command, input any) (*binding, error) {
	v := reflect.ValueOf(input).Elem()
	currentBinding := &binding{args: map[int]int{}, flags: map[string]int{}, rest: -1}

	for i := range v.NumField() {
		err := bindInputField(cmd, currentBinding, new(v.Type().Field(i)), v.Field(i), i)
		if err != nil {
			return nil, err
		}
	}

	return currentBinding, nil
}

// bindInputField registers the flag and/or argument one input field declares.
func bindInputField(
	cmd *spf.Command,
	currentBinding *binding,
	structField *reflect.StructField,
	value reflect.Value,
	index int,
) error {
	if tag, ok := structField.Tag.Lookup("flag"); ok {
		name, err := addFlag(cmd, structField, value, tag)
		if err != nil {
			return err
		}

		currentBinding.flags[name] = index
	}

	if tag, ok := structField.Tag.Lookup("arg"); ok {
		return addArg(currentBinding, structField, value, index, tag)
	}

	return nil
}

// addFlag registers the flag a `flag:"name,short,usage"` field declares and returns its
// name.
func addFlag(
	cmd *spf.Command,
	structField *reflect.StructField,
	value reflect.Value,
	tag string,
) (string, error) {
	parts := strings.SplitN(tag, ",", flagTagFields)
	if len(parts) != flagTagFields || parts[0] == "" || len(parts[1]) > 1 {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"field %s: flag tag %q is not name,short,usage",
				structField.Name,
				tag,
			),
		)
	}

	name, short, usage := parts[0], parts[1], parts[usageFieldIndex]

	if !registerFlagVar(cmd.Flags(), value, name, short, usage) {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"field %s: unsupported flag type %s",
				structField.Name,
				structField.Type,
			),
		)
	}

	if strings.Contains(structField.Tag.Get("jsonschema"), "required") {
		return name, apperr.Wrap(cmd.MarkFlagRequired(name), apperr.CodeInternal,
			"mark flag "+name+" required")
	}

	return name, nil
}

// registerFlagVar binds the field by its type; false means the type is unsupported.
func registerFlagVar(
	flagSet *pflag.FlagSet,
	fv reflect.Value,
	name, short, usage string,
) bool {
	switch text := fv.Addr().Interface().(type) {
	case *bool:
		flagSet.BoolVarP(text, name, short, *text, usage)
	case *string:
		flagSet.StringVarP(text, name, short, *text, usage)
	case *int:
		flagSet.IntVarP(text, name, short, *text, usage)
	case *time.Duration:
		flagSet.DurationVarP(text, name, short, *text, usage)
	case *[]string:
		flagSet.StringSliceVarP(text, name, short, *text, usage)
	default:
		return false
	}

	return true
}

// addArg registers the positional slot an `arg:"n"` field (field index i) declares.
func addArg(
	currentBinding *binding,
	structField *reflect.StructField,
	value reflect.Value,
	index int,
	tag string,
) error {
	count, err := strconv.Atoi(tag)
	if err != nil || count < 0 {
		return apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("field %s: arg tag %q is not an index", structField.Name, tag))
	}

	switch value.Interface().(type) {
	case string:
	case []string:
		currentBinding.rest = count
	default:
		return apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"field %s: unsupported argument type %s",
				structField.Name,
				structField.Type,
			),
		)
	}

	currentBinding.args[count] = index

	return nil
}
