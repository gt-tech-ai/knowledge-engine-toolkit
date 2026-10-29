package types

// Guard kinds: how a guard row checks the repository.
const (
	// GuardMatch checks file text: every Require pattern must match a line of every
	// selected file, and no Forbid pattern may match any.
	GuardMatch = "match"

	// GuardYAML checks a YAML path in every selected file: each node it selects must equal
	// Expect, or at least one must exist when Expect is empty.
	GuardYAML = "yaml"

	// GuardExpr evaluates a CEL expression over the selected files' text and documents,
	// and over the objects Render renders when it is set.
	GuardExpr = "expr"

	// GuardGo runs the Go guard the product registers under the row's name.
	GuardGo = "go"
)

// GuardRow is one repository check, as data.
type GuardRow struct {
	// Expect is the value a GuardYAML row's nodes must hold.
	Expect string `yaml:"expect" json:"expect,omitempty"`

	// Expr is a GuardExpr row's CEL expression. It sees `files` (the selected files, in
	// path order, each {path, text, docs}), `file` (the same, keyed by path), `objects`
	// (the rendered objects' documents), `vars` (the expansion row) and `now` (the run's
	// time, a timestamp). It returns a bool, a list of failure messages, or a list of
	// {file, line, message} maps.
	Expr string `yaml:"expr" json:"expr,omitempty"`

	// Verb is the command that owns the guard (check, test-k8s).
	Verb string `yaml:"verb" json:"verb"`

	// Group is the phase the guard runs in; empty means Verb.
	Group string `yaml:"group" json:"group,omitempty"`

	// Kind is GuardMatch, GuardYAML, GuardExpr or GuardGo.
	Kind string `yaml:"kind" json:"kind"`

	// Each names the product expander whose rows expand this one, one guard per row.
	Each string `yaml:"each" json:"each,omitempty"`

	// Message explains a failure and how to fix it; `{placeholder}`s expand.
	Message string `yaml:"message" json:"message,omitempty"`

	// Path is the YAML path a GuardYAML row queries (see YAMLView.Query).
	Path string `yaml:"path" json:"path,omitempty"`

	// Name identifies the guard; it names the guard's step. `{placeholder}`s expand.
	Name string `yaml:"name" json:"name"`

	// Func names the Go guard a GuardGo row runs; empty means Name, so one Go guard can
	// serve several rows.
	Func string `yaml:"func" json:"func,omitempty"`

	// Tool names the binary a GuardGo row shells, so the tool's version keys its cache.
	Tool string `yaml:"tool" json:"tool,omitempty"`

	// Inputs selects the files whose content keys a GuardGo row's cached pass: an
	// expansion whose inputs passed before is skipped and reported cached; a failure is
	// never cached. `{placeholder}`s expand. Empty inputs never cache.
	Inputs FileSelector `yaml:"inputs" json:"inputs,omitzero"`

	// Render, when its ChartDir is set, renders a chart whose objects the expression reads.
	Render RenderSpec `yaml:"render" json:"render,omitzero"`

	// Forbid are regular expressions no line of a selected file may match.
	Forbid []string `yaml:"forbid" json:"forbid,omitempty"`

	// Tags are the labels suites select guards by.
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// Require are regular expressions a GuardMatch row needs in every selected file.
	Require []string `yaml:"require" json:"require,omitempty"`

	// Names, when set, splits a GuardMatch row into one guard per Require pattern,
	// Names[i] naming Require[i].
	Names []string `yaml:"names" json:"names,omitempty"`

	// Files selects the files the guard reads.
	Files FileSelector `yaml:"files" json:"files,omitzero"`

	// SkipComments ignores full-line comments by file type when matching: `#` in YAML,
	// shell and Dockerfiles; `#` and `//` in Terraform and HCL; `//` in Alloy.
	SkipComments bool `yaml:"skip_comments" json:"skip_comments,omitempty"`

	// Literal treats Require and Forbid as plain substrings rather than regular
	// expressions.
	Literal bool `yaml:"literal" json:"literal,omitempty"`

	// Optional passes a guard whose selector picks no file; by default that fails.
	Optional bool `yaml:"optional" json:"optional,omitempty"`
}

// EntryMeta is what a suite selects a catalog entry by.
type EntryMeta struct {
	// Name identifies the entry.
	Name string

	// Verb is the command that owns it.
	Verb string

	// Group is the phase it runs in.
	Group string

	// Tags are its labels.
	Tags []string

	// Tools are the binaries the entry shells: what provisioning installs for a suite.
	Tools []string
}
