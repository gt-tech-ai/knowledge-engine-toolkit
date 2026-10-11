package listquery

import (
	"encoding/json"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxFilterDepth bounds $and/$or nesting so a crafted deeply-nested filter is a clean
// CodeInvalidInput rather than unbounded recursion (a stack-overflow panic that recover
// cannot catch).
const maxFilterDepth = 32

// opToken maps a JSON wire operator token to the core FilterOperator (v1 set);
// ok is false for an unknown token.
func opToken(token string) (op types.FilterOperator, ok bool) {
	switch token {
	case "$eq":
		return types.OpEq, true
	case "$ne":
		return types.OpNe, true
	case "$in":
		return types.OpIn, true
	case "$not_in":
		return types.OpNotIn, true
	case "$like":
		return types.OpLike, true
	case "$gt":
		return types.OpGt, true
	case "$gte":
		return types.OpGte, true
	case "$lt":
		return types.OpLt, true
	case "$lte":
		return types.OpLte, true
	default:
		return op, false
	}
}

// Parse compiles a JSON filter string, gated by the resource allow-list m, into the core
// Filter contract. Blank input (or "{}") yields an empty CompositeFilter (IsEmpty() true)
// — a store applies no WHERE. Malformed JSON, an unknown/duplicate operator, a
// non-allow-listed field, a type-incompatible value, or nesting past maxFilterDepth is a
// CodeInvalidInput error (ARCHITECTURE.md#error-codes). The result is the Filter
// interface — a FilterClause for a single comparison, a CompositeFilter for $and/$or —
// never *FilterClause, since a top-level $and/$or is composite.
func Parse(m *Map, jsonStr string) (types.Filter, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return types.CompositeFilter{}, nil // no filter → IsEmpty
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &node); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "filter: invalid JSON")
	}
	if len(node) == 0 {
		return types.CompositeFilter{}, nil // "{}" → no filter → IsEmpty
	}
	return parseNode(m, node, 0)
}

// parseNode walks one filter object (exactly one operator key) into a Filter, bounding
// recursion at maxFilterDepth.
func parseNode(m *Map, node map[string]json.RawMessage, depth int) (types.Filter, error) {
	if depth > maxFilterDepth {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: nesting exceeds max depth",
		)
	}
	if len(node) != 1 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: each object must carry exactly one operator",
		)
	}
	for key, raw := range node {
		if key == "$and" || key == "$or" {
			return parseComposite(m, key, raw, depth)
		}
		op, ok := opToken(key)
		if !ok {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"filter: unknown operator "+key,
			)
		}
		return parseClause(m, op, raw)
	}
	// Unreachable given the len(node) == 1 guard; a coded error (never nil,nil) if reached.
	return nil, apperr.New(apperr.CodeInvalidInput, "filter: empty object")
}

// parseComposite walks a $and/$or array into a CompositeFilter.
func parseComposite(
	m *Map,
	key string,
	raw json.RawMessage,
	depth int,
) (types.Filter, error) {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"filter: "+key+" must be an array of filters",
		)
	}
	if len(arr) == 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: "+key+" must not be empty",
		)
	}
	subs := make([]types.Filter, 0, len(arr))
	for _, sub := range arr {
		f, err := parseNode(m, sub, depth+1)
		if err != nil {
			return nil, err
		}
		subs = append(subs, f)
	}
	if key == "$and" {
		return types.CompositeFilter{And: subs}, nil
	}
	return types.CompositeFilter{Or: subs}, nil
}

// parseClause walks a comparison operator body ({field: value}) into a FilterClause,
// validating the field against the allow-list and the value against the field's type.
func parseClause(
	m *Map,
	op types.FilterOperator,
	raw json.RawMessage,
) (types.Filter, error) {
	var fieldVal map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fieldVal); err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"filter: operator body must be {field: value}",
		)
	}
	if len(fieldVal) != 1 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: operator body must carry exactly one field",
		)
	}
	for name, rawV := range fieldVal {
		field, ok := m.Lookup(name)
		if !ok {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"filter: field not allow-listed: "+name,
			)
		}
		value, err := decodeValue(op, rawV, field.Type)
		if err != nil {
			return nil, err
		}
		return types.FilterClause{Field: field.Name, Operator: op, Value: value}, nil
	}
	// Unreachable given the len(fieldVal) == 1 guard; a coded error (never nil,nil) if
	// reached.
	return nil, apperr.New(apperr.CodeInvalidInput, "filter: empty operator body")
}

// decodeValue decodes an operator operand: an array of type-compatible elements for
// in/not_in (an empty array is valid — a matches-nothing clause), otherwise a scalar.
func decodeValue(
	op types.FilterOperator,
	raw json.RawMessage,
	ft FieldType,
) (any, error) {
	if op == types.OpIn || op == types.OpNotIn {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"filter: in/not_in requires an array",
			)
		}
		out := make([]any, 0, len(arr))
		for _, el := range arr {
			v, err := decodeScalar(el, ft)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return decodeScalar(raw, ft)
}

// decodeScalar decodes a single JSON value into the Go value for the field type,
// rejecting a null or type-incompatible value with CodeInvalidInput.
func decodeScalar(raw json.RawMessage, ft FieldType) (any, error) {
	mismatch := func() error {
		return apperr.New(
			apperr.CodeInvalidInput,
			"filter: value type does not match field type",
		)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, mismatch()
	}
	switch ft {
	case FieldString, FieldID:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, mismatch()
		}
		return s, nil
	case FieldInt:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, mismatch()
		}
		return n, nil
	case FieldFloat:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, mismatch()
		}
		return f, nil
	case FieldBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, mismatch()
		}
		return b, nil
	case FieldTime:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, mismatch()
		}
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"filter: time value must be RFC3339",
			)
		}
		return ts, nil
	default:
		return nil, mismatch()
	}
}
