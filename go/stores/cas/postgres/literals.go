package postgres

const (
	// maxIdentifierLen is PostgreSQL's longest identifier, in bytes.
	maxIdentifierLen = 63

	// defaultNodesTable names the node table when Config leaves it empty.
	defaultNodesTable = "cas_nodes"

	// defaultRefsTable names the ref table when Config leaves it empty.
	defaultRefsTable = "cas_refs"
)
