package types

import "time"

// Capture values: how a procedure step's output becomes a variable.
const (
	// CaptureText is the trimmed standard output.
	CaptureText = "text"

	// CaptureLines is the standard output's non-empty trimmed lines.
	CaptureLines = "lines"

	// CaptureJSON is the standard output decoded as JSON.
	CaptureJSON = "json"

	// CaptureExit is the exit status: 0, the status the command exited with, or 1 when
	// it failed without one. A non-zero status does not fail the step (it is reported as
	// a warning naming the status). A streamed step may capture this way: its output
	// reaches the operator and its status the variable.
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

	// Inputs are the variables a caller supplies. Every procedure also sees `os` and
	// `arch`, the platform it runs on (runtime.GOOS / GOARCH), unless an input names
	// them.
	Inputs []string `yaml:"inputs" json:"inputs,omitempty"`

	// Vars are derived variables, each a CEL expression over the inputs and the vars
	// before it, evaluated in order before the first step.
	Vars []ProcVar `yaml:"vars" json:"vars,omitempty"`

	// Steps run in order; a failed step stops the procedure unless it is advisory.
	Steps []ProcStep `yaml:"steps" json:"steps"`

	// Finally are steps that run after Steps whatever their outcome — after a failure,
	// an error or a cancellation too — on a context detached from the run's
	// cancellation and bounded by FinallyTimeout: the teardown of what the steps set up
	// (a scratch database, a kubeconfig, containers an interrupted start left running).
	// They see the run's variables plus `failed` (a step failed or errored) and
	// `canceled` (the run was cancelled); their results follow the steps' in the run's,
	// and a failing one never stops the others.
	Finally []ProcStep `yaml:"finally" json:"finally,omitempty"`

	// FinallyTimeout bounds the Finally steps as a whole; zero is the engine's default.
	FinallyTimeout time.Duration `yaml:"finally_timeout" json:"finally_timeout,omitempty"`
}

// ProcVar is one derived procedure variable.
type ProcVar struct {
	// Name is the variable.
	Name string `yaml:"name" json:"name"`

	// Expr is the CEL expression that computes it.
	Expr string `yaml:"expr" json:"expr"`
}

// ProcStep is one procedure step. Exactly one of Tool, Exec, Step, Call, Parallel and
// Fail says what it runs. Text fields expand `{name}` from the variables — a
// `{name.key}` path reads into a map-valued variable, such as an Each item that is an
// object; an argument that is exactly `{name...}` (or `{name.key...}`) splats a list, and
// one that is exactly `${expr}` is a CEL expression (a list splats).
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

	// Sets names the variables a Go step sets, so later expressions may read them.
	Sets []string `yaml:"sets" json:"sets,omitempty"`

	// Call names a procedure to run with the current variables.
	Call string `yaml:"call" json:"call,omitempty"`

	// Fail makes the step an assertion: it runs nothing and fails with this message
	// (expanded like Name), so a condition the inputs violate is a failed row under
	// When — a refused flag combination, a name with a space.
	Fail string `yaml:"fail" json:"fail,omitempty"`

	// Tool names the toolchain tool to invoke with Args.
	Tool string `yaml:"tool" json:"tool,omitempty"`

	// Exec is a command line the caller supplies, run as is: expanded like Args (so
	// `{command...}` splats an input list), its first element the executable and the rest
	// its arguments, with no toolchain lookup. Cwd, Env, Missing, Stream, Capture and
	// Mutating apply as for Tool; an empty expansion is CodeInvalidInput.
	Exec []string `yaml:"exec" json:"exec,omitempty"`

	// Cwd is the working directory, relative to the root; empty is the root.
	Cwd string `yaml:"cwd" json:"cwd,omitempty"`

	// Missing is "skip" to skip the step when its tool is not installed; empty (or any
	// other value) fails. It is expanded like Name, so a `{name}` placeholder lets an
	// earlier step choose the policy (a strict run fails, a local one skips).
	Missing string `yaml:"missing" json:"missing,omitempty"`

	// Args are the tool's arguments.
	Args []string `yaml:"args" json:"args,omitempty"`

	// Env are NAME=value entries added to the tool's environment, expanded like Args: a
	// `{name}` placeholder, a `{name...}` splat of a list, a `${expr}` list.
	Env []string `yaml:"env" json:"env,omitempty"`

	// Parallel are steps run concurrently.
	Parallel []ProcStep `yaml:"parallel" json:"parallel,omitempty"`

	// Advisory reports a failure as a warning, without stopping the procedure: a
	// best-effort step (a cleanup, a `pkill` that may match nothing).
	Advisory bool `yaml:"advisory" json:"advisory,omitempty"`

	// Mutating marks a step that changes state; a dry run reports it instead.
	Mutating bool `yaml:"mutating" json:"mutating,omitempty"`

	// Stream sends the tool's output to the operator as it runs (a long job's progress)
	// instead of buffering it for the result; a streamed step captures only its exit
	// status (CaptureExit).
	Stream bool `yaml:"stream" json:"stream,omitempty"`

	// Concurrent runs an Each step's items concurrently (NumCPU-capped) instead of in
	// order, like Parallel: every item runs even when one fails, the results keep the
	// items' order, and the items' variables do not propagate.
	Concurrent bool `yaml:"concurrent" json:"concurrent,omitempty"`
}

// Capture stores a step's output in a variable.
type Capture struct {
	// Var is the variable.
	Var string `yaml:"var" json:"var"`

	// As is CaptureText (the default), CaptureLines, CaptureJSON or CaptureExit.
	As string `yaml:"as" json:"as,omitempty"`
}
