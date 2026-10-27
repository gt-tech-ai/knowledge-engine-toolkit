package types

// Flag types a FlagSpec may declare.
const (
	// FlagBool is a boolean flag.
	FlagBool = "bool"

	// FlagString is a string flag.
	FlagString = "string"

	// FlagInt is an integer flag.
	FlagInt = "int"

	// FlagStrings is a repeatable, comma-splittable string list flag.
	FlagStrings = "strings"

	// FlagDuration is a time.Duration flag; its input value is nanoseconds.
	FlagDuration = "duration"
)

// CommandSpec is one command of a command-line tree as data: a group (no Op) prints its
// help, a leaf runs the registered operation Op with its flags and positional arguments
// as the input.
type CommandSpec struct {
	// Use is the usage line; its first word is the command's name.
	Use string `yaml:"use" json:"use"`

	// Short is the one-line help.
	Short string `yaml:"short" json:"short,omitempty"`

	// Long is the full help.
	Long string `yaml:"long" json:"long,omitempty"`

	// Example shows usage.
	Example string `yaml:"example" json:"example,omitempty"`

	// Group is the help section of the parent the command is listed under.
	Group string `yaml:"group" json:"group,omitempty"`

	// Op is the operation a leaf runs.
	Op string `yaml:"op" json:"op,omitempty"`

	// Aliases are other names for the command.
	Aliases []string `yaml:"aliases" json:"aliases,omitempty"`

	// Flags are the command's flags; persistent ones reach its subcommands.
	Flags []FlagSpec `yaml:"flags" json:"flags,omitempty"`

	// Commands are the subcommands.
	Commands []CommandSpec `yaml:"commands" json:"commands,omitempty"`

	// Args are the positional arguments.
	Args ArgSpec `yaml:"args" json:"args,omitzero"`

	// Hidden leaves the command out of help.
	Hidden bool `yaml:"hidden" json:"hidden,omitempty"`
}

// FlagSpec is one flag.
type FlagSpec struct {
	// Default is the flag's default; when set it is always part of the input.
	Default any `yaml:"default" json:"default,omitempty"`

	// Name is the long name.
	Name string `yaml:"name" json:"name"`

	// Short is the one-letter shorthand.
	Short string `yaml:"short" json:"short,omitempty"`

	// Type is FlagBool (the default), FlagString, FlagInt, FlagStrings or FlagDuration.
	Type string `yaml:"type" json:"type,omitempty"`

	// Usage is the help text.
	Usage string `yaml:"usage" json:"usage,omitempty"`

	// Field is the input key; empty is Name with dashes as underscores.
	Field string `yaml:"field" json:"field,omitempty"`

	// Hidden leaves the flag out of help.
	Hidden bool `yaml:"hidden" json:"hidden,omitempty"`

	// Persistent makes the flag reach every subcommand.
	Persistent bool `yaml:"persistent" json:"persistent,omitempty"`

	// Required fails a run that does not set the flag.
	Required bool `yaml:"required" json:"required,omitempty"`

	// Secret marks a credential: the input schema leaves it out and ops.Decode refuses
	// it, so a tool-calling front-end (MCP, a CronJob's arguments) never carries it; only
	// a front-end where a human types it (ops.DecodeLocal, the CLI) passes it in.
	Secret bool `yaml:"secret" json:"secret,omitempty"`
}

// ArgSpec is a command's positional arguments.
type ArgSpec struct {
	// Rest is the input key that takes the remaining arguments as a list.
	Rest string `yaml:"rest" json:"rest,omitempty"`

	// Names are the input keys of the positional arguments, in order.
	Names []string `yaml:"names" json:"names,omitempty"`

	// Min is how many arguments a run needs.
	Min int `yaml:"min" json:"min,omitempty"`
}

// FlagField returns the input key of f.
func (f *FlagSpec) FlagField() string {
	if f.Field != "" {
		return f.Field
	}
	out := []byte(f.Name)
	for i, c := range out {
		if c == '-' {
			out[i] = '_'
		}
	}
	return string(out)
}
