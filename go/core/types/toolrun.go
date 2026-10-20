package types

// Each values: how a tool run maps targets to invocations.
const (
	// EachTarget runs the tool once per selected target, fanned out.
	EachTarget = "target"

	// EachRun runs the tool once, with every target in its arguments.
	EachRun = "run"
)

// Cwd values: where a tool run's invocations run.
const (
	// CwdRoot runs in the codebase root.
	CwdRoot = "root"

	// CwdTarget runs in the target's directory (EachTarget only).
	CwdTarget = "target"
)

// Missing values: what a tool run does when its tool is not installed.
const (
	// MissingSkip reports one skipped step carrying the tool's install hint.
	MissingSkip = "skip"

	// MissingFail reports one failed step carrying the tool's install hint.
	MissingFail = "fail"
)

// ToolRun is one external tool invocation, as data: which tool, with which arguments,
// over which targets, and how its output becomes a verdict.
type ToolRun struct {
	// Name identifies the run; it names the run's steps.
	Name string `yaml:"name" json:"name"`

	// Verb is the command that owns the run (lint, fmt, check).
	Verb string `yaml:"verb" json:"verb"`

	// Tool names the toolchain entry that supplies the binary, its version and its
	// install hint.
	Tool string `yaml:"tool" json:"tool"`

	// Each is EachTarget or EachRun; empty means EachRun.
	Each string `yaml:"each" json:"each,omitempty"`

	// Cwd is CwdRoot or CwdTarget; empty means CwdRoot.
	Cwd string `yaml:"cwd" json:"cwd,omitempty"`

	// Missing is MissingSkip or MissingFail; empty means MissingSkip.
	Missing string `yaml:"missing" json:"missing,omitempty"`

	// Parse names the parser kind that turns output into a verdict; empty means the
	// exit code alone decides.
	Parse string `yaml:"parse" json:"parse,omitempty"`

	// Targets selects what the tool runs over.
	Targets TargetSpec `yaml:"targets" json:"targets,omitzero"`

	// Tags are the labels suites select runs by (lint, go, infra).
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// Args are the tool's arguments; `{target}` is replaced by the target (EachTarget)
	// or expands to every target (EachRun).
	Args []string `yaml:"args" json:"args,omitempty"`

	// FixArgs are appended to Args when the run is asked to fix what it finds.
	FixArgs []string `yaml:"fix_args" json:"fix_args,omitempty"`

	// Inputs selects the files whose content keys the step cache; `{target}` in a root,
	// path or glob is replaced by the target. Empty inputs mean the run is never cached.
	Inputs FileSelector `yaml:"inputs" json:"inputs,omitzero"`
}

// TargetSpec selects a tool run's targets.
type TargetSpec struct {
	// Kind names the target selector (files, go_modules, python_projects,
	// ts_workspace, staged_files); empty means the run has no targets.
	Kind string `yaml:"kind" json:"kind,omitempty"`

	// Glob filters the files kinds' paths (files, staged_files).
	Glob string `yaml:"glob" json:"glob,omitempty"`

	// WithPackages keeps only Go modules that contain a Go file of their own.
	WithPackages bool `yaml:"with_packages" json:"with_packages,omitempty"`

	// SkipHeredoc drops Dockerfiles whose COPY, RUN or ADD instruction uses a heredoc.
	SkipHeredoc bool `yaml:"skip_heredoc" json:"skip_heredoc,omitempty"`
}
