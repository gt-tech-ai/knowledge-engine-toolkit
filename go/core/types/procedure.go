package types

// Capture values: how a procedure step's output becomes a variable.
const (
	// CaptureText is the trimmed standard output.
	CaptureText = "text"

	// CaptureLines is the standard output's non-empty trimmed lines.
	CaptureLines = "lines"

	// CaptureJSON is the standard output decoded as JSON.
	CaptureJSON = "json"

	// CaptureExit is the exit status (0 or 1); a non-zero exit does not fail the step.
	CaptureExit = "exit"
)

// Procedure is an ordered list of steps run as data: tool invocations over variables,
// with captured output, CEL conditions and loops, confirmations, sub-procedures and
// registered Go steps.
type Procedure struct {
	// Name identifies the procedure; operations and Call steps name it.
	Name string `yaml:"name" json:"name"`

	// Title labels the procedure in output; empty uses Name.
	Title string `yaml:"title" json:"title,omitempty"`

	// Group is the phase its steps report under; empty uses Name.
	Group string `yaml:"group" json:"group,omitempty"`

	// Inputs are the variables a caller supplies.
	Inputs []string `yaml:"inputs" json:"inputs,omitempty"`

	// Vars are derived variables, each a CEL expression over the inputs and the vars
	// before it, evaluated in order before the first step.
	Vars []ProcVar `yaml:"vars" json:"vars,omitempty"`

	// Steps run in order; a failed step stops the procedure unless it is advisory.
	Steps []ProcStep `yaml:"steps" json:"steps"`
}

// ProcVar is one derived procedure variable.
type ProcVar struct {
	// Name is the variable.
	Name string `yaml:"name" json:"name"`

	// Expr is the CEL expression that computes it.
	Expr string `yaml:"expr" json:"expr"`
}

// ProcStep is one procedure step. Exactly one of Tool, Step, Call and Parallel says
// what it runs. Text fields expand `{name}` from the variables; an argument that is
// exactly `{name...}` splats a list variable, and one that is exactly `${expr}` is a CEL
// expression (a list splats).
type ProcStep struct {
	// Capture stores the step's output in a variable.
	Capture *Capture `yaml:"capture" json:"capture,omitempty"`

	// Name labels the step's result.
	Name string `yaml:"name" json:"name"`

	// When is a CEL condition; false skips the step.
	When string `yaml:"when" json:"when,omitempty"`

	// Each is a CEL list; the step runs once per item, bound to As.
	Each string `yaml:"each" json:"each,omitempty"`

	// As names the Each item variable; empty means "item".
	As string `yaml:"as" json:"as,omitempty"`

	// Confirm asks this question first; a no cancels the procedure.
	Confirm string `yaml:"confirm" json:"confirm,omitempty"`

	// Step names a registered Go step.
	Step string `yaml:"step" json:"step,omitempty"`

	// Call names a procedure to run with the current variables.
	Call string `yaml:"call" json:"call,omitempty"`

	// Tool names the toolchain tool to invoke with Args.
	Tool string `yaml:"tool" json:"tool,omitempty"`

	// Cwd is the working directory, relative to the root; empty is the root.
	Cwd string `yaml:"cwd" json:"cwd,omitempty"`

	// Missing is "skip" to skip the step when its tool is not installed; empty fails.
	Missing string `yaml:"missing" json:"missing,omitempty"`

	// Args are the tool's arguments.
	Args []string `yaml:"args" json:"args,omitempty"`

	// Env are NAME=value entries added to the tool's environment.
	Env []string `yaml:"env" json:"env,omitempty"`

	// Parallel are steps run concurrently.
	Parallel []ProcStep `yaml:"parallel" json:"parallel,omitempty"`

	// Advisory reports a failure without stopping the procedure.
	Advisory bool `yaml:"advisory" json:"advisory,omitempty"`

	// Mutating marks a step that changes state; a dry run reports it instead.
	Mutating bool `yaml:"mutating" json:"mutating,omitempty"`
}

// Capture stores a step's output in a variable.
type Capture struct {
	// Var is the variable.
	Var string `yaml:"var" json:"var"`

	// As is CaptureText (the default), CaptureLines, CaptureJSON or CaptureExit.
	As string `yaml:"as" json:"as,omitempty"`
}
