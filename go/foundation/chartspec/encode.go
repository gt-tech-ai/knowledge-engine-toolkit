package chartspec

import (
	"strconv"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
// spec must not be nil.
func Encode(spec *types.ChartSpec) (Tree, error) {
	encoderValue := encoder{nodes: map[Hash]Node{}}

	root, err := encoderValue.spec(spec)
	if err != nil {
		return Tree{}, err
	}

	return Tree{Nodes: encoderValue.nodes, Root: root}, nil
}

// put hashes n and records it.
func (e *encoder) put(node Node) (Hash, error) {
	hash, err := node.Hash()
	if err != nil {
		return Hash{}, err
	}

	e.nodes[hash] = node

	return hash, nil
}

// setField records a scalar member unless it is the zero value.
func setField[T comparable](node *Node, name string, value T) {
	var zero T

	if value == zero {
		return
	}

	if node.Fields == nil {
		node.Fields = map[string]any{}
	}

	node.Fields[name] = value
}

// setChild records a child reference unless it is absent.
func setChild(node *Node, name string, hash Hash) {
	if hash.IsZero() {
		return
	}

	if node.Children == nil {
		node.Children = map[string]Hash{}
	}

	node.Children[name] = hash
}

// setList records a repeated child unless it is empty.
func setList(node *Node, name string, hashs []Hash) {
	if len(hashs) == 0 {
		return
	}

	if node.Lists == nil {
		node.Lists = map[string][]Hash{}
	}

	node.Lists[name] = hashs
}

// spec encodes the root node and everything under it.
func (e *encoder) spec(sRef *types.ChartSpec) (Hash, error) {
	chartSpec := *sRef

	node := Node{Kind: kindSpec}
	setField(&node, memberCube, chartSpec.Cube)
	setField(&node, memberGrain, string(chartSpec.Grain))
	setField(&node, memberMark, string(chartSpec.Mark))

	children := []struct {
		build func() (Hash, error)
		name  string
	}{
		{func() (Hash, error) { return e.shelf(chartSpec.Rows, "rows") }, memberRows},
		{
			func() (Hash, error) { return e.shelf(chartSpec.Columns, "columns") },
			memberColumns,
		},
		{
			func() (Hash, error) { return e.encodings(chartSpec.Encodings) },
			memberEncodings,
		},
		{
			func() (Hash, error) { return e.timeRange(chartSpec.TimeRange) },
			memberTimeRange,
		},
		{func() (Hash, error) { return e.filter(chartSpec.FilterTree) }, memberFilter},
	}
	for _, item := range children {
		h, err := item.build()
		if err != nil {
			return Hash{}, err
		}

		setChild(&node, item.name, h)
	}

	detail, err := e.detailList(chartSpec.Detail)
	if err != nil {
		return Hash{}, err
	}

	setList(&node, memberDetail, detail)

	sorts, err := e.sortList(chartSpec.Sort)
	if err != nil {
		return Hash{}, err
	}

	setList(&node, memberSort, sorts)

	return e.put(node)
}

// detailList encodes the detail field references.
func (e *encoder) detailList(detail []types.FieldRef) ([]Hash, error) {
	out := make([]Hash, 0, len(detail))

	for i := range detail {
		h, err := e.fieldRef(&detail[i])
		if err != nil {
			return nil, err
		}

		out = append(out, h)
	}

	return out, nil
}

// sortList encodes the sort directives.
func (e *encoder) sortList(sorts []types.OrderField) ([]Hash, error) {
	out := make([]Hash, 0, len(sorts))

	for _, o := range sorts {
		sn := Node{Kind: kindSort}
		setField(&sn, memberField, o.Field)
		setField(&sn, memberDesc, o.Desc)

		h, err := e.put(sn)
		if err != nil {
			return nil, err
		}

		out = append(out, h)
	}

	return out, nil
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
func (e *encoder) shelf(algebraExpr *types.AlgebraExpr, path string) (Hash, error) {
	if algebraExpr == nil {
		return Hash{}, nil
	}

	if algebraExpr.Field != nil {
		return e.fieldRef(algebraExpr.Field)
	}

	node := Node{Kind: kindShelf}
	setField(&node, memberOp, string(algebraExpr.Op))

	args := make([]Hash, 0, len(algebraExpr.Args))
	for i, arg := range algebraExpr.Args {
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

	setList(&node, memberArgs, args)

	return e.put(node)
}

// encodings encodes the encoding channels; all four unset is absent.
func (e *encoder) encodings(enc types.Encodings) (Hash, error) {
	node := Node{Kind: kindEncodings}

	for _, item := range []struct {
		ref  *types.FieldRef
		name string
	}{
		{enc.Color, memberColor},
		{enc.Size, memberSize},
		{enc.Shape, memberShape},
		{enc.Label, memberLabel},
	} {
		h, err := e.fieldRef(item.ref)
		if err != nil {
			return Hash{}, err
		}

		setChild(&node, item.name, h)
	}

	if node.Children == nil {
		return Hash{}, nil
	}

	return e.put(node)
}

// timeRange encodes the window as UTC RFC 3339 strings; both bounds zero is absent.
func (e *encoder) timeRange(timeRangeValue types.TimeRange) (Hash, error) {
	if timeRangeValue.From.IsZero() && timeRangeValue.To.IsZero() {
		return Hash{}, nil
	}

	node := Node{Kind: kindTimeRange}

	for _, b := range []struct {
		t    time.Time
		name string
	}{{timeRangeValue.From, memberFrom}, {timeRangeValue.To, memberTo}} {
		if !b.t.IsZero() {
			setField(&node, b.name, b.t.UTC().Format(time.RFC3339Nano))
		}
	}

	return e.put(node)
}

// filter encodes a filter tree; nil is absent and the zero node is an empty group.
func (e *encoder) filter(f *types.FilterNode) (Hash, error) {
	if f == nil {
		return Hash{}, nil
	}

	return e.filterNode(*f)
}

// filterNode encodes one filter node and its members.
func (e *encoder) filterNode(filterNodeValue types.FilterNode) (Hash, error) {
	if filterArmsSet(&filterNodeValue) > 1 {
		return Hash{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: a filter node must set at most one of clause, and, or")
	}

	if c := filterNodeValue.Clause; c != nil {
		return e.filterClause(*c)
	}

	node := Node{Kind: kindFilterGroup}
	members := filterNodeValue.And

	switch {
	case filterNodeValue.And != nil:
		setField(&node, memberOp, groupAnd)
	case filterNodeValue.Or != nil:
		setField(&node, memberOp, groupOr)

		members = filterNodeValue.Or
	default:
		// No other value changes the outcome.
	}

	hashs := make([]Hash, 0, len(members))

	for _, m := range members {
		h, err := e.filterNode(m)
		if err != nil {
			return Hash{}, err
		}

		hashs = append(hashs, h)
	}

	setList(&node, memberMembers, hashs)

	return e.put(node)
}

// filterArmsSet counts which of a filter node's clause, and, or arms are set.
func filterArmsSet(f *types.FilterNode) int {
	set := 0

	for _, armSet := range []bool{f.Clause != nil, f.And != nil, f.Or != nil} {
		if armSet {
			set++
		}
	}

	return set
}

// filterClause encodes one comparison. An unknown operator is CodeInvalidInput, so a
// stored tree always parses; a nil []any operand is stored as the empty array it means
// (a matches-nothing in/not_in), since a typed nil would read back as absent.
func (e *encoder) filterClause(filterNodeClause types.FilterNodeClause) (Hash, error) {
	if !filterNodeClause.Op.Known() {
		return Hash{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: unknown filter operator "+string(filterNodeClause.Op),
		)
	}

	node := Node{Kind: kindFilterClause, Fields: map[string]any{}}
	setField(&node, memberField, filterNodeClause.Field)
	setField(&node, memberOp, string(filterNodeClause.Op))

	value := filterNodeClause.Value
	if arr, ok := value.([]any); ok && arr == nil {
		value = []any{}
	}

	if !absent(value) {
		node.Fields[memberValue] = value
	}

	return e.put(node)
}
