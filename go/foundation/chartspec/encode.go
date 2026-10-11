package chartspec

import (
	"strconv"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The node kinds of a ChartSpec tree. A kind fixes which fields, children and lists a
// node may carry; renaming one re-addresses every stored spec.
const (
	// kindSpec is the root: fields cube, grain, mark; children rows, columns,
	// encodings, time_range, filter; lists detail, sort.
	kindSpec = "chart_spec"
	// kindShelf is an operator shelf expression: field op; list args.
	kindShelf = "chart_shelf"
	// kindFieldRef is a field reference, and a leaf shelf expression: fields field, agg.
	kindFieldRef = "chart_field_ref"
	// kindEncodings holds the encoding channels: children color, size, shape, label.
	kindEncodings = "chart_encodings"
	// kindTimeRange is the time window: fields from, to (UTC, RFC 3339).
	kindTimeRange = "chart_time_range"
	// kindSort is one sort key: fields field, desc.
	kindSort = "sort"
	// kindFilterGroup is an $and/$or group, or the empty filter when op is absent:
	// field op; list members.
	kindFilterGroup = "filter_group"
	// kindFilterClause is one comparison: fields field, op, value.
	kindFilterClause = "filter_clause"
)

// The member names nodes use, shared by Encode, Decode and Diff.
const (
	memberCube      = "cube"
	memberGrain     = "grain"
	memberMark      = "mark"
	memberRows      = "rows"
	memberColumns   = "columns"
	memberEncodings = "encodings"
	memberTimeRange = "time_range"
	memberFilter    = "filter"
	memberDetail    = "detail"
	memberSort      = "sort"
	memberOp        = "op"
	memberArgs      = "args"
	memberField     = "field"
	memberAgg       = "agg"
	memberColor     = "color"
	memberSize      = "size"
	memberShape     = "shape"
	memberLabel     = "label"
	memberFrom      = "from"
	memberTo        = "to"
	memberDesc      = "desc"
	memberMembers   = "members"
	memberValue     = "value"
)

// The op values of a filter_group node.
const (
	groupAnd = "and"
	groupOr  = "or"
)

// Tree is a ChartSpec as content-addressed nodes: every distinct node keyed by its hash,
// and the root's hash. Equal subtrees share one node.
type Tree struct {
	// Nodes holds every node of the tree by hash.
	Nodes map[Hash]Node

	// Root is the hash of the chart_spec node.
	Root Hash
}

// encoder accumulates the nodes of one Encode call.
type encoder struct {
	// nodes collects each node under its hash.
	nodes map[Hash]Node
}

// Encode splits spec into content-addressed nodes. A struct member at its zero value is
// left out of its node, except a filter clause's value, which only nil omits. The
// compiled Filter is not encoded: FilterTree is the stored filter, which Decode returns
// for the caller to Parse again. A nil shelf operand or a filter node with more than one
// arm set is CodeInvalidInput.
func Encode(spec types.ChartSpec) (Tree, error) {
	e := encoder{nodes: map[Hash]Node{}}
	root, err := e.spec(spec)
	if err != nil {
		return Tree{}, err
	}
	return Tree{Nodes: e.nodes, Root: root}, nil
}

// put hashes n and records it.
func (e *encoder) put(n Node) (Hash, error) {
	h, err := n.Hash()
	if err != nil {
		return Hash{}, err
	}
	e.nodes[h] = n
	return h, nil
}

// setField records a scalar member unless it is the zero value.
func setField[T comparable](n *Node, name string, v T) {
	var zero T
	if v == zero {
		return
	}
	if n.Fields == nil {
		n.Fields = map[string]any{}
	}
	n.Fields[name] = v
}

// setChild records a child reference unless it is absent.
func setChild(n *Node, name string, h Hash) {
	if h.IsZero() {
		return
	}
	if n.Children == nil {
		n.Children = map[string]Hash{}
	}
	n.Children[name] = h
}

// setList records a repeated child unless it is empty.
func setList(n *Node, name string, hs []Hash) {
	if len(hs) == 0 {
		return
	}
	if n.Lists == nil {
		n.Lists = map[string][]Hash{}
	}
	n.Lists[name] = hs
}

// spec encodes the root node and everything under it.
func (e *encoder) spec(s types.ChartSpec) (Hash, error) {
	n := Node{Kind: kindSpec}
	setField(&n, memberCube, s.Cube)
	setField(&n, memberGrain, string(s.Grain))
	setField(&n, memberMark, string(s.Mark))
	children := []struct {
		build func() (Hash, error)
		name  string
	}{
		{func() (Hash, error) { return e.shelf(s.Rows, "rows") }, memberRows},
		{func() (Hash, error) { return e.shelf(s.Columns, "columns") }, memberColumns},
		{func() (Hash, error) { return e.encodings(s.Encodings) }, memberEncodings},
		{func() (Hash, error) { return e.timeRange(s.TimeRange) }, memberTimeRange},
		{func() (Hash, error) { return e.filter(s.FilterTree) }, memberFilter},
	}
	for _, c := range children {
		h, err := c.build()
		if err != nil {
			return Hash{}, err
		}
		setChild(&n, c.name, h)
	}
	detail := make([]Hash, 0, len(s.Detail))
	for _, ref := range s.Detail {
		h, err := e.fieldRef(&ref)
		if err != nil {
			return Hash{}, err
		}
		detail = append(detail, h)
	}
	setList(&n, memberDetail, detail)
	sorts := make([]Hash, 0, len(s.Sort))
	for _, o := range s.Sort {
		sn := Node{Kind: kindSort}
		setField(&sn, memberField, o.Field)
		setField(&sn, memberDesc, o.Desc)
		h, err := e.put(sn)
		if err != nil {
			return Hash{}, err
		}
		sorts = append(sorts, h)
	}
	setList(&n, memberSort, sorts)
	return e.put(n)
}

// fieldRef encodes a field reference; nil is absent.
func (e *encoder) fieldRef(ref *types.FieldRef) (Hash, error) {
	if ref == nil {
		return Hash{}, nil
	}
	n := Node{Kind: kindFieldRef}
	setField(&n, memberField, ref.Name)
	setField(&n, memberAgg, string(ref.Agg))
	return e.put(n)
}

// shelf encodes a shelf expression at path: a leaf as a field reference, an operator
// as a chart_shelf over its operands. nil is absent.
func (e *encoder) shelf(x *types.AlgebraExpr, path string) (Hash, error) {
	if x == nil {
		return Hash{}, nil
	}
	if x.Field != nil {
		return e.fieldRef(x.Field)
	}
	n := Node{Kind: kindShelf}
	setField(&n, memberOp, string(x.Op))
	args := make([]Hash, 0, len(x.Args))
	for i, arg := range x.Args {
		argPath := path + "." + memberArgs + "[" + strconv.Itoa(i) + "]"
		if arg == nil {
			return Hash{}, apperr.New(apperr.CodeInvalidInput,
				"chartspec: nil shelf operand at "+argPath)
		}
		h, err := e.shelf(arg, argPath)
		if err != nil {
			return Hash{}, err
		}
		args = append(args, h)
	}
	setList(&n, memberArgs, args)
	return e.put(n)
}

// encodings encodes the encoding channels; all four unset is absent.
func (e *encoder) encodings(enc types.Encodings) (Hash, error) {
	n := Node{Kind: kindEncodings}
	for _, c := range []struct {
		ref  *types.FieldRef
		name string
	}{
		{enc.Color, memberColor},
		{enc.Size, memberSize},
		{enc.Shape, memberShape},
		{enc.Label, memberLabel},
	} {
		h, err := e.fieldRef(c.ref)
		if err != nil {
			return Hash{}, err
		}
		setChild(&n, c.name, h)
	}
	if n.Children == nil {
		return Hash{}, nil
	}
	return e.put(n)
}

// timeRange encodes the window as UTC RFC 3339 strings; both bounds zero is absent.
func (e *encoder) timeRange(tr types.TimeRange) (Hash, error) {
	if tr.From.IsZero() && tr.To.IsZero() {
		return Hash{}, nil
	}
	n := Node{Kind: kindTimeRange}
	for _, b := range []struct {
		t    time.Time
		name string
	}{{tr.From, memberFrom}, {tr.To, memberTo}} {
		if !b.t.IsZero() {
			setField(&n, b.name, b.t.UTC().Format(time.RFC3339Nano))
		}
	}
	return e.put(n)
}

// filter encodes a filter tree; nil is absent and the zero node is an empty group.
func (e *encoder) filter(f *types.FilterNode) (Hash, error) {
	if f == nil {
		return Hash{}, nil
	}
	return e.filterNode(*f)
}

// filterNode encodes one filter node and its members.
func (e *encoder) filterNode(f types.FilterNode) (Hash, error) {
	set := 0
	for _, armSet := range []bool{f.Clause != nil, f.And != nil, f.Or != nil} {
		if armSet {
			set++
		}
	}
	if set > 1 {
		return Hash{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: a filter node must set at most one of clause, and, or")
	}
	if c := f.Clause; c != nil {
		n := Node{Kind: kindFilterClause, Fields: map[string]any{}}
		setField(&n, memberField, c.Field)
		setField(&n, memberOp, string(c.Op))
		if c.Value != nil {
			n.Fields[memberValue] = c.Value
		}
		return e.put(n)
	}
	n := Node{Kind: kindFilterGroup}
	members := f.And
	switch {
	case f.And != nil:
		setField(&n, memberOp, groupAnd)
	case f.Or != nil:
		setField(&n, memberOp, groupOr)
		members = f.Or
	}
	hs := make([]Hash, 0, len(members))
	for _, m := range members {
		h, err := e.filterNode(m)
		if err != nil {
			return Hash{}, err
		}
		hs = append(hs, h)
	}
	setList(&n, memberMembers, hs)
	return e.put(n)
}
