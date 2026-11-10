package okf

// Rules are a caller's conformance rules on top of the OKF minimum.
type Rules struct {
	// RequiredKeys maps each known type to the frontmatter keys it must carry. When
	// non-empty, its keys are the only accepted type values.
	RequiredKeys map[string][]string

	// RootIndex is the path (as Doc.Path names it) of the bundle-root index.md, the
	// one index.md allowed an `okf_version` frontmatter key.
	RootIndex string
}

// Finding is one conformance problem.
type Finding struct {
	// Path is the file the finding is in.
	Path string

	// Rule names the broken rule: no-frontmatter, unknown-type, missing-key,
	// reserved-name, index-frontmatter, log-heading or log-order.
	Rule string

	// Message says what is wrong and how to fix it.
	Message string

	// Line is the 1-based line; 1 for a frontmatter-level finding.
	Line int
}
