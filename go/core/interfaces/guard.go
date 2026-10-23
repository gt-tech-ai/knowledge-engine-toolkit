package interfaces

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"

// Guard is a repository check a suite can select: a probe over the codebase plus the
// verb, group and tags suites match.
type Guard interface {
	Probe[Codebase]

	// Meta returns the guard's selection metadata.
	Meta() types.EntryMeta
}
