package types

// Artifacts are a generator's outputs: file contents by codebase-relative path.
type Artifacts map[string][]byte

// FileAction is what applying a scaffold plan does to one file.
type FileAction int

const (
	// FileCreate writes a file that does not exist.
	FileCreate FileAction = iota

	// FileUpdate overwrites a file whose content differs.
	FileUpdate

	// FileKeep leaves a file as it is.
	FileKeep

	// FileConflict is a file the plan cannot update safely; applying the plan fails.
	FileConflict
)

// String names the action.
func (a FileAction) String() string {
	switch a {
	case FileCreate:
		return "create"
	case FileUpdate:
		return "update"
	case FileKeep:
		return "keep"
	case FileConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// FilePolicy is how a scaffolded file treats an existing copy.
type FilePolicy int

const (
	// PolicyOnce writes the file only when it does not exist; an existing file is the
	// owner's and is kept.
	PolicyOnce FilePolicy = iota

	// PolicyRegenerate overwrites the file whenever its content differs.
	PolicyRegenerate

	// PolicyPreserve regenerates the file above its preserve marker line and keeps the
	// existing file's lines from the marker down.
	PolicyPreserve
)

// FileEntry is one file of a scaffold plan.
type FileEntry struct {
	// Path is the codebase-relative path.
	Path string

	// Reason explains a conflict.
	Reason string

	// Content is what applying the entry writes.
	Content []byte

	// Action is what applying the entry does.
	Action FileAction

	// Policy is the file's policy.
	Policy FilePolicy
}

// FilePlan is a scaffold's planned file changes, in path order.
type FilePlan struct {
	// Entries are the plan's files.
	Entries []FileEntry
}

// Generator row kinds.
const (
	// GenRun runs a tool that writes its outputs under the `{out}` directory.
	GenRun = "run"

	// GenTemplate renders a text/template over a registered data provider.
	GenTemplate = "template"
)

// GenRow is a generator as data.
type GenRow struct {
	// Run is the tool run of a GenRun row; its arguments name the output directory
	// `{out}`.
	Run ToolRun `yaml:"run" json:"run,omitzero"`

	// Inputs select the source files the outputs derive from.
	Inputs FileSelector `yaml:"inputs" json:"inputs,omitzero"`

	// Name identifies the generator.
	Name string `yaml:"name" json:"name"`

	// Kind is GenRun or GenTemplate.
	Kind string `yaml:"kind" json:"kind"`

	// Out is the repository-relative output: the directory `{out}` stands for (GenRun)
	// or the rendered file (GenTemplate).
	Out string `yaml:"out" json:"out"`

	// Template is the repository-relative template file of a GenTemplate row.
	Template string `yaml:"template" json:"template,omitempty"`

	// Data names the registered data provider a GenTemplate row renders over.
	Data string `yaml:"data" json:"data,omitempty"`
}
