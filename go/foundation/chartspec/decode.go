package chartspec

import (
	"slices"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// NodeGetter fetches a stored node by hash, reporting false when the store lacks it.
type NodeGetter func(Hash) (Node, bool)

// decoder rebuilds a spec from nodes fetched through get.
type decoder struct {
	// get fetches each node.
	get NodeGetter
}

// Decode rebuilds the ChartSpec rooted at root. Every fetched node is re-hashed: a
// missing node is CodeNotFound, a node whose content does not hash to its address is
// CodeInternal, and an unknown kind, a kind out of place, a malformed field or nesting
// past MaxShelfDepth or types.MaxFilterDepth is CodeInvalidInput. The result carries
// FilterTree but no compiled Filter; the time range comes back in UTC.
func Decode(root Hash, get NodeGetter) (types.ChartSpec, error) {
	d := decoder{get: get}

	return d.spec(root)
}

// knownKind reports whether k is one of the node kinds Encode writes.
func knownKind(k string) bool {
	switch k {
	case kindSpec, kindShelf, kindFieldRef, kindEncodings,
		kindTimeRange, kindSort, kindFilterGroup, kindFilterClause:
		return true
	default:
		return false
	}
}

// fetch loads and verifies the node at h, requiring one of kinds, or any known kind
// when kinds is empty.
func (d decoder) fetch(h Hash, kinds ...string) (Node, error) {
	n, ok := d.get(h)
	if !ok {
		return Node{}, apperr.New(
			apperr.CodeNotFound,
			"chartspec: node "+h.String()+" not found",
		)
	}

	got, err := n.Hash()
	if err != nil {
		return Node{}, err
	}

	if got != h {
		return Node{}, apperr.New(apperr.CodeInternal,
			"chartspec: node "+h.String()+" does not match its content")
	}

	wanted := slices.Contains(kinds, n.Kind)
	if len(kinds) == 0 {
		wanted = knownKind(n.Kind)
	}

	if !wanted {
		return Node{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: unexpected node kind "+n.Kind)
	}

	return n, nil
}

// fieldOf reads a typed field of n; absent is the zero value, another type is
// CodeInvalidInput.
func fieldOf[T any](n Node, name string) (T, error) {
	var zero T

	v, ok := n.Fields[name]

	if !ok || v == nil {
		return zero, nil
	}

	t, ok := v.(T)
	if !ok {
		return zero, apperr.New(apperr.CodeInvalidInput,
			"chartspec: field "+name+" of "+n.Kind+" has the wrong type")
	}

	return t, nil
}

// spec decodes the root node.
func (d decoder) spec(root Hash) (types.ChartSpec, error) {
	n, err := d.fetch(root, kindSpec)
	if err != nil {
		return types.ChartSpec{}, err
	}

	var s types.ChartSpec

	s.Cube, err = fieldOf[string](n, memberCube)
	if err != nil {
		return types.ChartSpec{}, err
	}

	grain, err := fieldOf[string](n, memberGrain)
	if err != nil {
		return types.ChartSpec{}, err
	}

	mark, err := fieldOf[string](n, memberMark)
	if err != nil {
		return types.ChartSpec{}, err
	}

	s.Grain, s.Mark = types.Grain(grain), types.Mark(mark)

	err = d.specChildren(n, &s)
	if err != nil {
		return types.ChartSpec{}, err
	}

	err = d.specLists(n, &s)
	if err != nil {
		return types.ChartSpec{}, err
	}

	return s, nil
}

// specChildren decodes the root's singular children into s.
func (d decoder) specChildren(n Node, s *types.ChartSpec) error {
	var err error

	s.Rows, err = d.optionalShelf(n.Children[memberRows])
	if err != nil {
		return err
	}

	s.Columns, err = d.optionalShelf(n.Children[memberColumns])
	if err != nil {
		return err
	}

	s.Encodings, err = d.encodings(n.Children[memberEncodings])
	if err != nil {
		return err
	}

	s.TimeRange, err = d.timeRange(n.Children[memberTimeRange])
	if err != nil {
		return err
	}

	s.FilterTree, err = d.optionalFilter(n.Children[memberFilter])

	return err
}

// optionalShelf decodes a root shelf; the zero hash is no shelf.
func (d decoder) optionalShelf(h Hash) (*types.AlgebraExpr, error) {
	if h.IsZero() {
		var none *types.AlgebraExpr

		return none, nil
	}

	return d.shelf(h, 1)
}

// optionalFilter decodes the root filter; the zero hash is no filter.
func (d decoder) optionalFilter(h Hash) (*types.FilterNode, error) {
	if h.IsZero() {
		var none *types.FilterNode

		return none, nil
	}

	f, err := d.filterNode(h, 0)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

// specLists decodes the root's detail and sort lists into s; an empty list is nil.
func (d decoder) specLists(n Node, s *types.ChartSpec) error {
	for _, h := range n.Lists[memberDetail] {
		ref, err := d.fieldRef(h)
		if err != nil {
			return err
		}

		s.Detail = append(s.Detail, *ref)
	}

	for _, h := range n.Lists[memberSort] {
		order, err := d.orderField(h)
		if err != nil {
			return err
		}

		s.Sort = append(s.Sort, order)
	}

	return nil
}

// orderField decodes one chart_sort node.
func (d decoder) orderField(h Hash) (types.OrderField, error) {
	sn, err := d.fetch(h, kindSort)
	if err != nil {
		return types.OrderField{}, err
	}

	field, err := fieldOf[string](sn, memberField)
	if err != nil {
		return types.OrderField{}, err
	}

	desc, err := fieldOf[bool](sn, memberDesc)
	if err != nil {
		return types.OrderField{}, err
	}

	return types.OrderField{Field: field, Desc: desc}, nil
}

// fieldRef decodes a chart_field_ref node.
func (d decoder) fieldRef(h Hash) (*types.FieldRef, error) {
	n, err := d.fetch(h, kindFieldRef)
	if err != nil {
		return nil, err
	}

	return fieldRefOf(n)
}

// fieldRefOf reads a fetched chart_field_ref node.
func fieldRefOf(n Node) (*types.FieldRef, error) {
	name, err := fieldOf[string](n, memberField)
	if err != nil {
		return nil, err
	}

	agg, err := fieldOf[string](n, memberAgg)
	if err != nil {
		return nil, err
	}

	return &types.FieldRef{Name: name, Agg: types.Aggregate(agg)}, nil
}

// shelf decodes the shelf expression at h, at depth; callers skip an absent shelf.
func (d decoder) shelf(h Hash, depth int) (*types.AlgebraExpr, error) {
	if depth > MaxShelfDepth {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: shelf nesting exceeds max depth",
		)
	}

	n, err := d.fetch(h, kindShelf, kindFieldRef)
	if err != nil {
		return nil, err
	}

	if n.Kind == kindFieldRef {
		return shelfLeaf(n)
	}

	op, err := fieldOf[string](n, memberOp)
	if err != nil {
		return nil, err
	}

	x := &types.AlgebraExpr{Op: types.AlgebraOp(op)}

	for _, ah := range n.Lists[memberArgs] {
		arg, err := d.shelf(ah, depth+1)
		if err != nil {
			return nil, err
		}

		x.Args = append(x.Args, arg)
	}

	return x, nil
}

// shelfLeaf wraps a fetched chart_field_ref node as a shelf leaf.
func shelfLeaf(n Node) (*types.AlgebraExpr, error) {
	ref, err := fieldRefOf(n)
	if err != nil {
		return nil, err
	}

	return &types.AlgebraExpr{Field: ref}, nil
}

// encodings decodes the encoding channels; the zero hash is no encodings.
func (d decoder) encodings(h Hash) (types.Encodings, error) {
	if h.IsZero() {
		return types.Encodings{}, nil
	}

	n, err := d.fetch(h, kindEncodings)
	if err != nil {
		return types.Encodings{}, err
	}

	var enc types.Encodings

	for _, c := range []struct {
		dst  **types.FieldRef
		name string
	}{
		{&enc.Color, memberColor},
		{&enc.Size, memberSize},
		{&enc.Shape, memberShape},
		{&enc.Label, memberLabel},
	} {
		*c.dst, err = d.optionalFieldRef(n.Children[c.name])
		if err != nil {
			return types.Encodings{}, err
		}
	}

	return enc, nil
}

// optionalFieldRef decodes an encoding channel; the zero hash is an unset channel.
func (d decoder) optionalFieldRef(h Hash) (*types.FieldRef, error) {
	if h.IsZero() {
		var none *types.FieldRef

		return none, nil
	}

	return d.fieldRef(h)
}

// timeRange decodes the window; the zero hash is the zero window.
func (d decoder) timeRange(h Hash) (types.TimeRange, error) {
	if h.IsZero() {
		return types.TimeRange{}, nil
	}

	n, err := d.fetch(h, kindTimeRange)
	if err != nil {
		return types.TimeRange{}, err
	}

	from, err := timeBound(n, memberFrom)
	if err != nil {
		return types.TimeRange{}, err
	}

	to, err := timeBound(n, memberTo)
	if err != nil {
		return types.TimeRange{}, err
	}

	return types.TimeRange{From: from, To: to}, nil
}

// timeBound reads one window bound; an empty value is the zero time.
func timeBound(n Node, name string) (time.Time, error) {
	s, err := fieldOf[string](n, name)
	if err != nil || s == "" {
		return time.Time{}, err
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			"chartspec: invalid time "+name)
	}

	return t, nil
}

// filterNode decodes a filter node at depth; a group's members come back non-nil even
// when empty, so an empty $and or $or survives the round trip.
func (d decoder) filterNode(h Hash, depth int) (types.FilterNode, error) {
	if depth > types.MaxFilterDepth {
		return types.FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: filter nesting exceeds max depth")
	}

	n, err := d.fetch(h, kindFilterGroup, kindFilterClause)
	if err != nil {
		return types.FilterNode{}, err
	}

	op, err := fieldOf[string](n, memberOp)
	if err != nil {
		return types.FilterNode{}, err
	}

	if n.Kind == kindFilterClause {
		return filterClauseOf(n, op)
	}

	if op == "" {
		return types.FilterNode{}, nil
	}

	members, err := d.filterMembers(n, depth)
	if err != nil {
		return types.FilterNode{}, err
	}

	return filterGroup(op, members)
}

// filterClauseOf reads a fetched chart_filter_clause node.
func filterClauseOf(n Node, op string) (types.FilterNode, error) {
	field, err := fieldOf[string](n, memberField)
	if err != nil {
		return types.FilterNode{}, err
	}

	return types.FilterNode{Clause: &types.FilterNodeClause{
		Field: field, Op: types.FilterOperator(op), Value: n.Fields[memberValue],
	}}, nil
}

// filterMembers decodes a group's members, non-nil even when empty.
func (d decoder) filterMembers(n Node, depth int) ([]types.FilterNode, error) {
	members := make([]types.FilterNode, 0, len(n.Lists[memberMembers]))

	for _, mh := range n.Lists[memberMembers] {
		m, err := d.filterNode(mh, depth+1)
		if err != nil {
			return nil, err
		}

		members = append(members, m)
	}

	return members, nil
}

// filterGroup builds the group node op names; an unknown op is CodeInvalidInput.
func filterGroup(op string, members []types.FilterNode) (types.FilterNode, error) {
	switch op {
	case groupAnd:
		return types.FilterNode{And: members}, nil
	case groupOr:
		return types.FilterNode{Or: members}, nil
	default:
		return types.FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: unknown filter group op "+op)
	}
}
