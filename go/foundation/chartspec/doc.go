// Package chartspec is the pure visual-algebra compiler of the analytics engine.
//
// A types.ChartSpec places cube fields on shelves as table-algebra expressions
// (concat +, cross ×, nest /). Parse decodes and validates the JSON spec against a
// cube's listquery allow-list (field roles and permitted aggregates); Normalize
// turns a shelf into its tuple table; Compile derives the one AggregateQuery the
// store runs, the pane table and a mark per pane; Drill swaps a dimension for its
// child in the cube's hierarchy; and the reducer (Partial, Merge, Finalize)
// combines additive partials and mergeable DDSketch quantile sketches. The
// package does no I/O and imports only core and foundation/listquery.
package chartspec
