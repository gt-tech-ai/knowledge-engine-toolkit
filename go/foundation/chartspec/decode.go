package chartspec

import (
	"slices"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
func (d decoder) fetch(hash Hash, kinds ...string) (Node, error) {
	node, ok := d.get(hash)
	if !ok {
		return Node{}, apperr.New(
			apperr.CodeNotFound,
			"chartspec: node "+hash.String()+" not found",
		)
	}

	got, err := node.Hash()
	if err != nil {
		return Node{}, err
	}

	if got != hash {
		return Node{}, apperr.New(apperr.CodeInternal,
			"chartspec: node "+hash.String()+" does not match its content")
	}

	wanted := slices.Contains(kinds, node.Kind)
	if len(kinds) == 0 {
		wanted = knownKind(node.Kind)
	}

	if !wanted {
		return Node{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: unexpected node kind "+node.Kind)
	}

	return node, nil
}

// fieldOf reads a typed field of n; absent is the zero value, another type is
// CodeInvalidInput.
func fieldOf[T any](node Node, name string) (T, error) {
	var zero T

	value, found := node.Fields[name]

	if !found || value == nil {
		return zero, nil
	}

	valueValue, found := value.(T)
	if !found {
		return zero, apperr.New(apperr.CodeInvalidInput,
			"chartspec: field "+name+" of "+node.Kind+" has the wrong type")
	}

	return valueValue, nil
}

// spec decodes the root node.
func (d decoder) spec(root Hash) (types.ChartSpec, error) {
	node, err := d.fetch(root, kindSpec)
	if err != nil {
		return types.ChartSpec{}, err
	}

	var chartSpec types.ChartSpec

	chartSpec.Cube, err = fieldOf[string](node, memberCube)
	if err != nil {
		return types.ChartSpec{}, err
	}

	grain, err := fieldOf[string](node, memberGrain)
	if err != nil {
		return types.ChartSpec{}, err
	}

	mark, err := fieldOf[string](node, memberMark)
	if err != nil {
		return types.ChartSpec{}, err
	}

	chartSpec.Grain, chartSpec.Mark = types.Grain(grain), types.Mark(mark)

	err = d.specChildren(node, &chartSpec)
	if err != nil {
		return types.ChartSpec{}, err
	}

	err = d.specLists(node, &chartSpec)
	if err != nil {
		return types.ChartSpec{}, err
	}

	return chartSpec, nil
}

// specChildren decodes the root's singular children into s.
func (d decoder) specChildren(node Node, chartSpec *types.ChartSpec) error {
	var err error

	chartSpec.Rows, err = d.optionalShelf(node.Children[memberRows])
	if err != nil {
		return err
	}

	chartSpec.Columns, err = d.optionalShelf(node.Children[memberColumns])
	if err != nil {
		return err
	}

	chartSpec.Encodings, err = d.encodings(node.Children[memberEncodings])
	if err != nil {
		return err
	}

	chartSpec.TimeRange, err = d.timeRange(node.Children[memberTimeRange])
	if err != nil {
		return err
	}

	chartSpec.FilterTree, err = d.optionalFilter(node.Children[memberFilter])

	return err
}

// optionalShelf decodes a root shelf; the zero hash is no shelf.
func (d decoder) optionalShelf(hash Hash) (*types.AlgebraExpr, error) {
	if hash.IsZero() {
		var none *types.AlgebraExpr

		return none, nil
	}

	return d.shelf(hash, 1)
}

// optionalFilter decodes the root filter; the zero hash is no filter.
func (d decoder) optionalFilter(hash Hash) (*types.FilterNode, error) {
	if hash.IsZero() {
		var none *types.FilterNode

		return none, nil
	}

	f, err := d.filterNode(hash, 0)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

// specLists decodes the root's detail and sort lists into s; an empty list is nil.
func (d decoder) specLists(node Node, chartSpec *types.ChartSpec) error {
	for _, h := range node.Lists[memberDetail] {
		ref, err := d.fieldRef(h)
		if err != nil {
			return err
		}

		chartSpec.Detail = append(chartSpec.Detail, *ref)
	}

	for _, h := range node.Lists[memberSort] {
		order, err := d.orderField(h)
		if err != nil {
			return err
		}

		chartSpec.Sort = append(chartSpec.Sort, order)
	}

	return nil
}

// orderField decodes one chart_sort node.
func (d decoder) orderField(h Hash) (types.OrderField, error) {
	node, err := d.fetch(h, kindSort)
	if err != nil {
		return types.OrderField{}, err
	}

	field, err := fieldOf[string](node, memberField)
	if err != nil {
		return types.OrderField{}, err
	}

	desc, err := fieldOf[bool](node, memberDesc)
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
func fieldRefOf(node Node) (*types.FieldRef, error) {
	name, err := fieldOf[string](node, memberField)
	if err != nil {
		return nil, err
	}

	agg, err := fieldOf[string](node, memberAgg)
	if err != nil {
		return nil, err
	}

	return &types.FieldRef{Name: name, Agg: types.Aggregate(agg)}, nil
}

// shelf decodes the shelf expression at h, at depth; callers skip an absent shelf.
func (d decoder) shelf(hash Hash, depth int) (*types.AlgebraExpr, error) {
	if depth > MaxShelfDepth {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: shelf nesting exceeds max depth",
		)
	}

	node, err := d.fetch(hash, kindShelf, kindFieldRef)
	if err != nil {
		return nil, err
	}

	if node.Kind == kindFieldRef {
		return shelfLeaf(node)
	}

	op, err := fieldOf[string](node, memberOp)
	if err != nil {
		return nil, err
	}

	algebraExpr := &types.AlgebraExpr{Op: types.AlgebraOp(op)}

	for _, ah := range node.Lists[memberArgs] {
		arg, err := d.shelf(ah, depth+1)
		if err != nil {
			return nil, err
		}

		algebraExpr.Args = append(algebraExpr.Args, arg)
	}

	return algebraExpr, nil
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

	node, err := d.fetch(h, kindEncodings)
	if err != nil {
		return types.Encodings{}, err
	}

	var enc types.Encodings

	for _, item := range []struct {
		dst  **types.FieldRef
		name string
	}{
		{&enc.Color, memberColor},
		{&enc.Size, memberSize},
		{&enc.Shape, memberShape},
		{&enc.Label, memberLabel},
	} {
		*item.dst, err = d.optionalFieldRef(node.Children[item.name])
		if err != nil {
			return types.Encodings{}, err
		}
	}

	return enc, nil
}

// optionalFieldRef decodes an encoding channel; the zero hash is an unset channel.
func (d decoder) optionalFieldRef(hash Hash) (*types.FieldRef, error) {
	if hash.IsZero() {
		var none *types.FieldRef

		return none, nil
	}

	return d.fieldRef(hash)
}

// timeRange decodes the window; the zero hash is the zero window.
func (d decoder) timeRange(h Hash) (types.TimeRange, error) {
	if h.IsZero() {
		return types.TimeRange{}, nil
	}

	node, err := d.fetch(h, kindTimeRange)
	if err != nil {
		return types.TimeRange{}, err
	}

	from, err := timeBound(node, memberFrom)
	if err != nil {
		return types.TimeRange{}, err
	}

	to, err := timeBound(node, memberTo)
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

	timeValue, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			"chartspec: invalid time "+name)
	}

	return timeValue, nil
}

// filterNode decodes a filter node at depth; a group's members come back non-nil even
// when empty, so an empty $and or $or survives the round trip.
func (d decoder) filterNode(hash Hash, depth int) (types.FilterNode, error) {
	if depth > types.MaxFilterDepth {
		return types.FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: filter nesting exceeds max depth")
	}

	node, err := d.fetch(hash, kindFilterGroup, kindFilterClause)
	if err != nil {
		return types.FilterNode{}, err
	}

	operation, err := fieldOf[string](node, memberOp)
	if err != nil {
		return types.FilterNode{}, err
	}

	if node.Kind == kindFilterClause {
		return filterClauseOf(node, operation)
	}

	if operation == "" {
		return types.FilterNode{}, nil
	}

	members, err := d.filterMembers(node, depth)
	if err != nil {
		return types.FilterNode{}, err
	}

	return filterGroup(operation, members)
}

// filterClauseOf reads a fetched chart_filter_clause node.
func filterClauseOf(node Node, operation string) (types.FilterNode, error) {
	field, err := fieldOf[string](node, memberField)
	if err != nil {
		return types.FilterNode{}, err
	}

	return types.FilterNode{Clause: &types.FilterNodeClause{
		Field: field,
		Op:    types.FilterOperator(operation),
		Value: node.Fields[memberValue],
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
func filterGroup(operation string, members []types.FilterNode) (types.FilterNode, error) {
	switch operation {
	case groupAnd:
		return types.FilterNode{And: members}, nil
	case groupOr:
		return types.FilterNode{Or: members}, nil
	default:
		return types.FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"chartspec: unknown filter group op "+operation)
	}
}
