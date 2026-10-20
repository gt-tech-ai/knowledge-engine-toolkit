// Package cobra is the command-line front-end of an operations registry
// (execution/ops): Command builds a Cobra command for one registered operation, its
// flags and positional arguments derived from the operation's input type, so a CLI and
// the other front-ends run the operation on the same input.
package cobra

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	spf "github.com/spf13/cobra"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// Options shapes a command.
type Options struct {
	// Observer receives the run's progress; nil reports nothing.
	Observer interfaces.ExecutionObserver

	// Use overrides the command's usage line; empty uses the operation's name.
	Use string
}

// Command returns the command that runs the operation named name. Each input field
// tagged `flag:"name,short,usage"` is a flag (bool, string, int, time.Duration or
// []string; a field also tagged jsonschema:"required" is a required flag), and each
// field tagged `arg:"n"` takes the n-th positional argument (a []string field takes it
// and the rest). An unknown or excluded operation fails like ops.Registry.Find, and an
// unsupported field type or a malformed tag is CodeInvalidInput naming the field.
func Command(reg *ops.Registry, name string, opts Options) (*spf.Command, error) {
	op, err := reg.Find(name)
	if err != nil {
		return nil, err
	}
	desc := op.Descriptor()
	use := opts.Use
	if use == "" {
		use = desc.Name
	}
	cmd := &spf.Command{Use: use, Short: desc.Summary, SilenceUsage: true}
	flags := op.NewInput()
	b, err := bind(cmd, flags)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
			fmt.Sprintf("operation %q", name))
	}
	cmd.Args = spf.RangeArgs(0, b.maxArgs())
	cmd.RunE = func(c *spf.Command, args []string) error {
		input := op.NewInput()
		dst := reflect.ValueOf(input).Elem()
		b.setFlags(c, reflect.ValueOf(flags).Elem(), dst)
		if err := b.positional(dst, args); err != nil {
			return err
		}
		return reg.Run(c.Context(), name, input, opts.Observer)
	}
	return cmd, nil
}

// binding is where a command's flags and positional arguments go. The flags parse into
// one input the command keeps; each run copies the flags set on its command line, and
// stores its positional arguments, into a fresh input.
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

// setFlags copies into dst each flag field set on c's command line, from the parsed src;
// an unset flag keeps dst's seeded default.
func (b *binding) setFlags(c *spf.Command, src, dst reflect.Value) {
	for name, i := range b.flags {
		if c.Flags().Changed(name) {
			dst.Field(i).Set(src.Field(i))
		}
	}
}

// positional stores args into their fields of dst.
func (b *binding) positional(dst reflect.Value, args []string) error {
	for i, a := range args {
		if b.rest >= 0 && i >= b.rest {
			f := dst.Field(b.args[b.rest])
			f.Set(reflect.Append(f, reflect.ValueOf(a)))
			continue
		}
		f, ok := b.args[i]
		if !ok {
			return apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("unexpected argument %q", a))
		}
		dst.Field(f).SetString(a)
	}
	return nil
}

// bind registers a flag or positional slot for each tagged field of *input.
func bind(cmd *spf.Command, input any) (*binding, error) {
	v := reflect.ValueOf(input).Elem()
	b := &binding{args: map[int]int{}, flags: map[string]int{}, rest: -1}
	for i := range v.NumField() {
		sf, fv := v.Type().Field(i), v.Field(i)
		if tag, ok := sf.Tag.Lookup("flag"); ok {
			name, err := addFlag(cmd, sf, fv, tag)
			if err != nil {
				return nil, err
			}
			b.flags[name] = i
		}
		if tag, ok := sf.Tag.Lookup("arg"); ok {
			if err := addArg(b, sf, fv, i, tag); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}

// addFlag registers the flag a `flag:"name,short,usage"` field declares and returns its
// name.
func addFlag(
	cmd *spf.Command,
	sf reflect.StructField,
	fv reflect.Value,
	tag string,
) (string, error) {
	parts := strings.SplitN(tag, ",", 3)
	if len(parts) != 3 || parts[0] == "" || len(parts[1]) > 1 {
		return "", apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("field %s: flag tag %q is not name,short,usage", sf.Name, tag))
	}
	name, short, usage := parts[0], parts[1], parts[2]
	fs := cmd.Flags()
	switch p := fv.Addr().Interface().(type) {
	case *bool:
		fs.BoolVarP(p, name, short, *p, usage)
	case *string:
		fs.StringVarP(p, name, short, *p, usage)
	case *int:
		fs.IntVarP(p, name, short, *p, usage)
	case *time.Duration:
		fs.DurationVarP(p, name, short, *p, usage)
	case *[]string:
		fs.StringSliceVarP(p, name, short, *p, usage)
	default:
		return "", apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("field %s: unsupported flag type %s", sf.Name, sf.Type))
	}
	if strings.Contains(sf.Tag.Get("jsonschema"), "required") {
		return name, apperr.Wrap(cmd.MarkFlagRequired(name), apperr.CodeInternal,
			"mark flag "+name+" required")
	}
	return name, nil
}

// addArg registers the positional slot an `arg:"n"` field (field index i) declares.
func addArg(
	b *binding,
	sf reflect.StructField,
	fv reflect.Value,
	i int,
	tag string,
) error {
	n, err := strconv.Atoi(tag)
	if err != nil || n < 0 {
		return apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("field %s: arg tag %q is not an index", sf.Name, tag))
	}
	switch fv.Interface().(type) {
	case string:
	case []string:
		b.rest = n
	default:
		return apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("field %s: unsupported argument type %s", sf.Name, sf.Type))
	}
	b.args[n] = i
	return nil
}
