// Package chartspec is the pure visual-algebra compiler of the analytics engine.
//
// A types.ChartSpec places cube fields on shelves as table-algebra expressions
// (concat +, cross ×, nest /). Parse decodes and validates the JSON spec against a
// cube's listquery allow-list (field roles and permitted aggregates); Normalize
// turns a shelf into its tuple table; Compile derives the one AggregateQuery the
// store runs, the pane table and a mark per pane; Drill swaps a dimension for its
// child in the cube's hierarchy; and the reducer (Partial, Merge, Finalize)
// combines additive partials and mergeable DDSketch quantile sketches.
//
// Encode splits a spec into content-addressed Nodes, each addressed by the SHA-256 of
// its RFC 8785 canonical JSON, so equal subtrees share one node and a leaf change
// re-addresses only the nodes on its path to the root. Decode rebuilds a spec from a
// root hash, verifying every node against its address, and Diff walks two roots and
// reports the changed paths, descending only where hashes differ. The package does no
// I/O: callers supply stored nodes through a NodeGetter.
package chartspec
