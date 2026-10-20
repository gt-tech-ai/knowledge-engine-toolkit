package types

import (
	"bytes"
	"go/ast"
	"go/token"
	"strings"
)

// File is one file of a repository, named by its slash-separated path relative to the
// repository root.
type File struct {
	// Path is the repository-relative, slash-separated path.
	Path string

	// Untracked marks a file that is present on disk but not yet tracked by the
	// repository's version control (always false for a source without one).
	Untracked bool
}

// FileSelector selects a subset of a repository's files. Every non-empty field narrows
// the selection; the zero selector selects every file except generated Go files.
type FileSelector struct {
	// Roots keeps files under any of these repository-relative directories.
	Roots []string `yaml:"roots" json:"roots,omitempty"`

	// Paths keeps exactly these repository-relative files.
	Paths []string `yaml:"paths" json:"paths,omitempty"`

	// Globs keeps files matching any of these patterns; `**` matches any number of path
	// segments and `*`, `?` and `[...]` match within one segment.
	Globs []string `yaml:"globs" json:"globs,omitempty"`

	// Exts keeps files whose name ends in one of these extensions (".go", ".yaml").
	Exts []string `yaml:"exts" json:"exts,omitempty"`

	// Exclude drops files matching any of these globs; an entry with no glob
	// metacharacter drops the file or directory it names and everything under it.
	Exclude []string `yaml:"exclude" json:"exclude,omitempty"`

	// TrackedOnly drops untracked files.
	TrackedOnly bool `yaml:"tracked_only" json:"tracked_only,omitempty"`

	// IncludeGenerated keeps generated Go files (a `Code generated … DO NOT EDIT.`
	// header, or a generated name such as `*.pb.go`), which are dropped by default.
	IncludeGenerated bool `yaml:"include_generated" json:"include_generated,omitempty"`
}

// IsZero reports whether s sets no field, so it is the default selector (every file
// except generated Go files) rather than one a caller configured.
func (s FileSelector) IsZero() bool {
	return len(s.Roots) == 0 && len(s.Paths) == 0 && len(s.Globs) == 0 &&
		len(s.Exts) == 0 && len(s.Exclude) == 0 && !s.TrackedOnly && !s.IncludeGenerated
}

// Text is one file's contents.
type Text struct {
	// Path is the repository-relative path the bytes were read from.
	Path string

	// Bytes is the file's contents.
	Bytes []byte

	// Binary reports a NUL byte in the first 8 KiB, git's text/binary heuristic.
	Binary bool
}

// Lines returns the file's lines without their terminators; a trailing newline does not
// add an empty last line.
func (t Text) Lines() []string {
	s := strings.TrimSuffix(strings.ReplaceAll(string(t.Bytes), "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// IsBinary reports whether data holds a NUL byte in its first 8 KiB.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0
}

// Hit is one line of a file that matched a pattern.
type Hit struct {
	// File is the repository-relative path of the matching file.
	File string

	// Text is the whole matching line.
	Text string

	// Pattern is the source of the pattern that matched.
	Pattern string

	// Line is the 1-based line number.
	Line int

	// Col is the 1-based byte column where the match starts.
	Col int
}

// Finding is one violation a check reports.
type Finding struct {
	// Check names the check that reported the finding.
	Check string `json:"check"`

	// File is the repository-relative path the finding is in; empty for a finding about
	// the repository as a whole.
	File string `json:"file,omitempty"`

	// Rule names the rule within the check that was violated.
	Rule string `json:"rule,omitempty"`

	// Message says what is wrong and, where it can, how to fix it.
	Message string `json:"message"`

	// Line is the 1-based line number; 0 when the finding has no line.
	Line int `json:"line,omitempty"`
}

// Verdict is the outcome of one check or probe.
type Verdict struct {
	// Detail carries diagnostic output (a tool's stdout and stderr, an error).
	Detail string

	// Findings are the violations found; a failing verdict usually has at least one.
	Findings []Finding

	// Status is the outcome.
	Status StepStatus

	// Transient marks a failure worth retrying (a timeout, a target not yet ready).
	Transient bool
}

// RenderSpec names one render of a chart: the chart directory, the values files in
// order, and the release identity.
type RenderSpec struct {
	// Set holds `--set` overrides, applied after the values files.
	Set map[string]string `yaml:"set" json:"set,omitempty"`

	// ChartDir is the repository-relative chart directory.
	ChartDir string `yaml:"chart_dir" json:"chart_dir"`

	// Release is the release name; empty uses the chart directory's base name.
	Release string `yaml:"release" json:"release,omitempty"`

	// Namespace is the release namespace; empty leaves it unset.
	Namespace string `yaml:"namespace" json:"namespace,omitempty"`

	// ValuesFiles are repository-relative values files, applied in order.
	ValuesFiles []string `yaml:"values_files" json:"values_files,omitempty"`
}

// Object is one rendered manifest document.
type Object struct {
	// Doc is the decoded document.
	Doc map[string]any

	// Kind is the document's kind.
	Kind string

	// Name is the document's metadata.name.
	Name string

	// Namespace is the document's metadata.namespace.
	Namespace string

	// Source is the template file the document was rendered from, when the renderer
	// reports one.
	Source string

	// Index is the document's 0-based position in the render output.
	Index int
}

// ParseMode selects how much of a Go file is parsed.
type ParseMode int

const (
	// ParseImports parses the package clause and imports only.
	ParseImports ParseMode = iota

	// ParseComments parses the whole file, comments included.
	ParseComments
)

// GoFile is one parsed Go file.
type GoFile struct {
	// FileSet positions the file's nodes; files parsed in one mode share it.
	FileSet *token.FileSet

	// AST is the parsed file.
	AST *ast.File

	// Path is the repository-relative path.
	Path string
}

// YAMLDoc names one document of a YAML file: its path and its 0-based position among
// the file's documents.
type YAMLDoc struct {
	// Path is the repository-relative path.
	Path string

	// Index is the document's 0-based position in the file.
	Index int
}

// YAMLMatch is one node a YAML path query selected.
type YAMLMatch struct {
	// Value is the node's value for a scalar; empty for a mapping or a sequence.
	Value string

	// Kind is "scalar", "mapping", "sequence" or "alias".
	Kind string

	// Line is the node's 1-based line in its file.
	Line int
}
