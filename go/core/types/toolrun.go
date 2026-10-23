package types

import "strings"

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

// Any other Cwd value is a slash-separated directory relative to the codebase root.

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
	// Missing is MissingSkip or MissingFail; empty means MissingSkip.
	Missing string `yaml:"missing" json:"missing,omitempty"`

	// Parse names the parser kind that turns output into a verdict; empty means the
	// exit code alone decides.
	Parse string `yaml:"parse" json:"parse,omitempty"`

	// Group is the gate phase the run's steps report under; empty means Verb.
	Group string `yaml:"group" json:"group,omitempty"`

	// Tool names the toolchain entry that supplies the binary, its version and its
	// install hint.
	Tool string `yaml:"tool" json:"tool"`

	// Each is EachTarget or EachRun; empty means EachRun.
	Each string `yaml:"each" json:"each,omitempty"`

	// Cwd is CwdRoot, CwdTarget or a directory relative to the codebase root; empty
	// means CwdRoot.
	Cwd string `yaml:"cwd" json:"cwd,omitempty"`

	// Verb is the command that owns the run (lint, fmt, check).
	Verb string `yaml:"verb" json:"verb"`

	// Name identifies the run; it names the run's steps.
	Name string `yaml:"name" json:"name"`

	// Args are the tool's arguments; `{target}` is replaced by the target (EachTarget)
	// or expands to every target (EachRun), and any other `{name}` by RunOptions.Vars.
	Args []string `yaml:"args" json:"args,omitempty"`

	// Tags are the labels suites select runs by (lint, go, infra).
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// FixArgs are appended to Args when the run is asked to fix what it finds.
	FixArgs []string `yaml:"fix_args" json:"fix_args,omitempty"`

	// Env are environment entries for the tool: NAME=value sets, -NAME removes an
	// inherited variable. Placeholders expand as in Args.
	Env []string `yaml:"env" json:"env,omitempty"`

	// Inputs selects the files whose content keys the step cache; `{target}` in a root,
	// path or glob is replaced by the target. Empty inputs mean the run is never cached.
	Inputs FileSelector `yaml:"inputs" json:"inputs,omitzero"`

	// Targets selects what the tool runs over.
	Targets TargetSpec `yaml:"targets" json:"targets,omitzero"`

	// Advisory reports a failed invocation as a warning that never fails the run.
	Advisory bool `yaml:"advisory" json:"advisory,omitempty"`
}

// RunOptions are what one execution of a tool run adds to its row.
type RunOptions struct {
	// Vars are the values of the `{name}` placeholders in the run's arguments (an image,
	// a commit range); a placeholder with no value refuses the run.
	Vars map[string]string

	// Fix appends the run's fix arguments.
	Fix bool

	// Strict fails a run whose tool is missing, whatever its Missing says.
	Strict bool

	// DryRun reports each mutating procedure step's command instead of running it;
	// read-only steps still run, so their captures feed later conditions.
	DryRun bool

	// Yes answers every procedure confirmation with yes.
	Yes bool
}

// Selection picks tool runs: a run matches when its verb is in Verbs, it carries every
// tag in Tags and at least one in AnyTags, its name is in Names, its name is not in
// Exclude, and it carries no tag in ExcludeTags. An empty list does not constrain.
type Selection struct {
	// Verbs are the verbs a run may have.
	Verbs []string `yaml:"verbs" json:"verbs,omitempty"`

	// Tags are the tags a run must all carry.
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// AnyTags are the tags a run must carry at least one of.
	AnyTags []string `yaml:"any_tags" json:"any_tags,omitempty"`

	// ExcludeTags are the tags a run must carry none of.
	ExcludeTags []string `yaml:"exclude_tags" json:"exclude_tags,omitempty"`

	// Names are the names a run may have.
	Names []string `yaml:"names" json:"names,omitempty"`

	// Exclude are the names a run may not have.
	Exclude []string `yaml:"exclude" json:"exclude,omitempty"`
}

// TargetSpec selects a tool run's targets.
type TargetSpec struct {
	// Kind names the target selector (files, go_modules, python_projects,
	// ts_workspace, staged_files); empty means the run has no targets.
	Kind string `yaml:"kind" json:"kind,omitempty"`

	// Glob filters the files kinds' paths (files, staged_files).
	Glob string `yaml:"glob" json:"glob,omitempty"`

	// Globs adds patterns to Glob for the files kinds: a path matching any is kept.
	Globs []string `yaml:"globs" json:"globs,omitempty"`

	// Roots keeps only the targets at or under one of these directories (every kind);
	// empty keeps them all.
	Roots []string `yaml:"roots" json:"roots,omitempty"`

	// Exclude drops the targets matching any of these globs (every kind; `**` spans
	// segments), e.g. generated modules.
	Exclude []string `yaml:"exclude" json:"exclude,omitempty"`

	// WithPackages keeps only Go modules that contain a Go file of their own.
	WithPackages bool `yaml:"with_packages" json:"with_packages,omitempty"`

	// SkipHeredoc drops Dockerfiles whose COPY, RUN or ADD instruction uses a heredoc.
	SkipHeredoc bool `yaml:"skip_heredoc" json:"skip_heredoc,omitempty"`
}

// MergeEnv applies extra to base, an environment in os.Environ form: NAME=value sets
// NAME (replacing an inherited value) and -NAME removes it. Order is kept, with new
// names appended.
func MergeEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	drop := map[string]bool{}
	for _, e := range extra {
		if name, ok := strings.CutPrefix(e, "-"); ok {
			drop[name] = true
		} else if name, _, ok := strings.Cut(e, "="); ok {
			drop[name] = true
		}
	}
	for _, e := range base {
		if name, _, _ := strings.Cut(e, "="); !drop[name] {
			out = append(out, e)
		}
	}
	for _, e := range extra {
		if !strings.HasPrefix(e, "-") {
			out = append(out, e)
		}
	}
	return out
}
