package types

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
