package types

import (
	"go/ast"
	"go/token"
)

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

// GoFile is one parsed Go file.
type GoFile struct {
	// FileSet positions the file's nodes; files parsed in one mode share it.
	FileSet *token.FileSet

	// AST is the parsed file.
	AST *ast.File

	// Path is the repository-relative path.
	Path string
}

// Document is one decoded document of a configuration file, in the one JSON-like shape
// every format decodes to: maps, lists, strings, numbers, booleans and nil.
type Document struct {
	// Value is the decoded document.
	Value any

	// Path is the repository-relative path.
	Path string

	// Format is the decoder: "yaml" (YAML and JSON, document by document), "hcl"
	// (Terraform's JSON syntax: blocks nested by type and labels, each a list of bodies,
	// literal values typed, every other expression its source text) or "alloy" (the same
	// shape; block names dotted, the label nested).
	Format string

	// Index is the document's 0-based position in the file; 0 for a single-document
	// format.
	Index int
}
