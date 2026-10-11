package chartspec

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"

// Tuple is one row (or column) of a shelf's normalized table: the field
// references along that header path, outermost first. Nested marks a tuple built
// by the nest operator: the reducer keeps only the combinations observed in the data.
type Tuple struct {
	// Fields are the field references of the header path, outermost first.
	Fields []types.FieldRef
	// Nested marks a tuple produced (at any level) by the nest operator.
	Nested bool
}

// Normalize returns the tuple table of a shelf expression: a leaf is one
// single-field tuple, concat is the union (operands' tuples in order), cross the
// Cartesian product, and nest the Cartesian product flagged Nested. An empty
// (nil) shelf normalizes to one empty tuple, the identity of the product.
func Normalize(s types.Shelf) []Tuple {
	if s == nil {
		return []Tuple{{}}
	}
	if s.Field != nil {
		return []Tuple{{Fields: []types.FieldRef{*s.Field}}}
	}
	switch s.Op {
	case types.AlgebraConcat:
		var out []Tuple
		for _, arg := range s.Args {
			out = append(out, Normalize(arg)...)
		}
		return out
	case types.AlgebraCross, types.AlgebraNest:
		out := []Tuple{{}}
		for _, arg := range s.Args {
			out = product(out, Normalize(arg), s.Op == types.AlgebraNest)
		}
		return out
	default:
		return nil
	}
}

// product returns every left tuple extended by every right tuple; nest flags the
// results Nested.
func product(left, right []Tuple, nest bool) []Tuple {
	out := make([]Tuple, 0, len(left)*len(right))
	for _, l := range left {
		for _, r := range right {
			fields := make([]types.FieldRef, 0, len(l.Fields)+len(r.Fields))
			fields = append(append(fields, l.Fields...), r.Fields...)
			out = append(out, Tuple{Fields: fields, Nested: nest || l.Nested || r.Nested})
		}
	}
	return out
}
