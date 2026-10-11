package types

import (
	"bytes"
	"encoding/json"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// MaxFilterDepth bounds $and/$or nesting in a filter tree, so a crafted deeply nested
// filter is a clean CodeInvalidInput rather than unbounded recursion.
const MaxFilterDepth = 32

// FilterNode is a filter tree before it is checked against a field allow-list: the
// structured form of the JSON filter grammar, shared by list queries and chart specs.
// At most one of Clause, And and Or is set; the zero node is the empty filter. Its JSON
// codec reads and writes the grammar: {"$<op>": {"<field>": value}} for a clause,
// {"$and": [...]} and {"$or": [...]} for a group, and {} for the empty filter.
type FilterNode struct {
	// Clause is a single comparison.
	Clause *FilterNodeClause

	// And holds the members that must all match.
	And []FilterNode

	// Or holds the members of which at least one must match.
	Or []FilterNode
}

// FilterNodeClause is one comparison in a FilterNode.
type FilterNodeClause struct {
	// Value is the operand as decoded: a json.Number for a number from JSON, a float64
	// from a structured source, a string, a bool, or a []any for in/not_in. Type checks
	// against the field happen when the tree is parsed.
	Value any

	// Field is the filtered field's name, not yet checked against an allow-list.
	Field string

	// Op is the comparison.
	Op FilterOperator
}

// The group keys of the filter grammar.
const (
	// filterAndKey is the key of an all-must-match group.
	filterAndKey = "$and"
	// filterOrKey is the key of an any-may-match group.
	filterOrKey = "$or"
)

// marshalFilter encodes one grammar object, coding a failure (an operand the JSON
// encoder cannot represent) as CodeInvalidInput.
func marshalFilter(v any) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "filter: encode")
	}
	return out, nil
}

// arms counts the arms n sets; a group counts as set when its slice is non-nil.
func (n FilterNode) arms() int {
	count := 0
	if n.Clause != nil {
		count++
	}
	if n.And != nil {
		count++
	}
	if n.Or != nil {
		count++
	}
	return count
}

// MarshalJSON writes n in the filter grammar. A node with more than one arm set is a
// CodeInvalidInput error.
func (n FilterNode) MarshalJSON() ([]byte, error) {
	switch {
	case n.arms() > 1:
		return nil, apperr.New(apperr.CodeInvalidInput,
			"filter: a node must set at most one of clause, and, or")
	case n.Clause != nil:
		return marshalFilter(map[string]map[string]any{
			"$" + string(n.Clause.Op): {n.Clause.Field: n.Clause.Value},
		})
	case n.And != nil:
		return marshalFilter(map[string][]FilterNode{filterAndKey: n.And})
	case n.Or != nil:
		return marshalFilter(map[string][]FilterNode{filterOrKey: n.Or})
	default:
		return []byte("{}"), nil
	}
}

// UnmarshalJSON reads n from the filter grammar, keeping numbers as json.Number. An
// object with more than one key, an unknown operator, an operator body with other than
// one field, a group that is not an array, or nesting past MaxFilterDepth is a
// CodeInvalidInput error. Field allow-lists and value types are not checked here.
func (n *FilterNode) UnmarshalJSON(data []byte) error {
	node, err := decodeFilterNode(data, 0)
	if err != nil {
		return err
	}
	*n = node
	return nil
}

// decodeFilterNode decodes one grammar object at the given nesting depth.
func decodeFilterNode(data []byte, depth int) (FilterNode, error) {
	if depth > MaxFilterDepth {
		return FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"filter: nesting exceeds max depth")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return FilterNode{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			"filter: a node must be a JSON object")
	}
	if len(obj) == 0 {
		return FilterNode{}, nil
	}
	if len(obj) != 1 {
		return FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"filter: each object must carry exactly one operator")
	}
	for key, raw := range obj {
		if key == filterAndKey || key == filterOrKey {
			return decodeFilterGroup(key, raw, depth)
		}
		return decodeFilterClause(key, raw)
	}
	return FilterNode{}, nil // unreachable: obj has exactly one key
}

// decodeFilterGroup decodes the array body of an $and or $or group.
func decodeFilterGroup(key string, raw json.RawMessage, depth int) (FilterNode, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return FilterNode{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			"filter: "+key+" must be an array of filters")
	}
	members := make([]FilterNode, 0, len(items))
	for _, item := range items {
		member, err := decodeFilterNode(item, depth+1)
		if err != nil {
			return FilterNode{}, err
		}
		members = append(members, member)
	}
	if key == filterAndKey {
		return FilterNode{And: members}, nil
	}
	return FilterNode{Or: members}, nil
}

// decodeFilterClause decodes a {"<field>": value} operator body for the operator token.
func decodeFilterClause(token string, raw json.RawMessage) (FilterNode, error) {
	op := FilterOperator(token[min(1, len(token)):])
	if token == "" || token[0] != '$' || !op.Known() {
		return FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"filter: unknown operator "+token)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return FilterNode{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			"filter: operator body must be {field: value}")
	}
	if len(body) != 1 {
		return FilterNode{}, apperr.New(apperr.CodeInvalidInput,
			"filter: operator body must carry exactly one field")
	}
	for field, rawValue := range body {
		dec := json.NewDecoder(bytes.NewReader(rawValue))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			return FilterNode{}, apperr.Wrap(err, apperr.CodeInvalidInput,
				"filter: invalid value")
		}
		return FilterNode{
			Clause: &FilterNodeClause{Field: field, Op: op, Value: value},
		}, nil
	}
	return FilterNode{}, nil // unreachable: body has exactly one field
}
