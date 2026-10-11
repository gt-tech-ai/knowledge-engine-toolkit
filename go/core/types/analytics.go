package types

import "time"

// Row is one stored partial row of an aggregate query's result: the grain-aligned
// time, the values of the query's group-by dimensions, and one encoded mergeable
// partial per requested measure (decode with foundation/chartspec.DecodePartial). Rows
// of the same group are merged by the reducer, in any order.
type Row struct {
	// TS is the grain-aligned time of the row.
	TS time.Time
	// Group holds the value of each of the query's GroupBy dimensions.
	Group map[string]string
	// Partials holds the encoded partial of each requested measure, by measure name.
	Partials map[string][]byte
}
