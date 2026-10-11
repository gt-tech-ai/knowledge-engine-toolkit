package listquery

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxSafeInt is the largest integer every client can carry exactly (2^53 - 1, the
// largest integer a float64 or a JavaScript number represents without rounding).
const maxSafeInt = 1<<53 - 1

// Parse compiles a JSON filter string, gated by the resource allow-list m, into the core
// Filter contract: it decodes the string into a types.FilterNode and parses that with
// ParseFilter. Blank input (or "{}") yields an empty CompositeFilter (IsEmpty() true) —
// a store applies no WHERE. Malformed JSON, or anything ParseFilter rejects, is a
// CodeInvalidInput error (ARCHITECTURE.md#error-codes).
func Parse(m *Map, jsonStr string) (types.Filter, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return types.CompositeFilter{}, nil // no filter → IsEmpty
	}
	var node types.FilterNode
	if err := json.Unmarshal([]byte(jsonStr), &node); err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeInvalidInput),
			"filter: invalid JSON",
		)
	}
	return ParseFilter(m, node)
}

// ParseFilter checks a filter tree against the resource allow-list m and builds the core
// Filter contract from it. The zero node yields an empty CompositeFilter. A node with
// other than one arm, an empty $and/$or, nesting past types.MaxFilterDepth, an empty or
// unknown operator, a non-allow-listed field, or a value that does not fit the field's
// type (in/not_in take a []any) is a CodeInvalidInput error. The result is a
// FilterClause for a single comparison and a CompositeFilter for $and/$or.
func ParseFilter(m *Map, n types.FilterNode) (types.Filter, error) {
	if n.Clause == nil && n.And == nil && n.Or == nil {
		return types.CompositeFilter{}, nil // no filter → IsEmpty
	}
	return parseNode(m, n, 0)
}

// parseNode walks one node (exactly one arm) into a Filter, bounding recursion at
// types.MaxFilterDepth.
func parseNode(m *Map, n types.FilterNode, depth int) (types.Filter, error) {
	if depth > types.MaxFilterDepth {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: nesting exceeds max depth",
		)
	}
	arms := 0
	for _, set := range []bool{n.Clause != nil, n.And != nil, n.Or != nil} {
		if set {
			arms++
		}
	}
	if arms != 1 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: each node must carry exactly one operator",
		)
	}
	switch {
	case n.Clause != nil:
		return parseClause(m, *n.Clause)
	case n.And != nil:
		return parseComposite(m, "$and", n.And, depth)
	default:
		return parseComposite(m, "$or", n.Or, depth)
	}
}

// parseComposite walks the members of a $and/$or group into a CompositeFilter.
func parseComposite(
	m *Map,
	key string,
	members []types.FilterNode,
	depth int,
) (types.Filter, error) {
	if len(members) == 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: "+key+" must not be empty",
		)
	}
	subs := make([]types.Filter, 0, len(members))
	for _, member := range members {
		f, err := parseNode(m, member, depth+1)
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

// parseClause checks one comparison: the operator against the known set, the field
// against the allow-list and the value against the field's type.
func parseClause(m *Map, c types.FilterNodeClause) (types.Filter, error) {
	if !c.Op.Known() {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: unknown operator "+string(c.Op),
		)
	}
	field, ok := m.Lookup(c.Field)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: field not allow-listed: "+c.Field,
		)
	}
	value, err := decodeValue(c.Op, c.Value, field.Type)
	if err != nil {
		return nil, err
	}
	return types.FilterClause{Field: field.Name, Operator: c.Op, Value: value}, nil
}

// decodeValue checks an operator operand: for in/not_in a []any of type-compatible
// elements (a structured source converts a typed slice first; an empty array is valid,
// a matches-nothing clause), otherwise a scalar.
func decodeValue(op types.FilterOperator, v any, ft FieldType) (any, error) {
	if op != types.OpIn && op != types.OpNotIn {
		return decodeScalar(v, ft)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: in/not_in requires a []any array",
		)
	}
	out := make([]any, 0, len(arr))
	for _, el := range arr {
		d, err := decodeScalar(el, ft)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// decodeScalar converts one operand into the Go value for the field type: a string for
// string/id, an int64 within ±(2^53 - 1) for int, a float64 for float, a bool, or an
// RFC 3339 time. A number may arrive as a json.Number (from JSON) or a float64 (from a
// structured source). A null or type-incompatible value is CodeInvalidInput.
func decodeScalar(v any, ft FieldType) (any, error) {
	mismatch := apperr.New(
		apperr.CodeInvalidInput,
		"filter: value type does not match field type",
	)
	switch ft {
	case FieldString, FieldID:
		if s, ok := v.(string); ok {
			return s, nil
		}
	case FieldInt:
		if n, ok := safeInt(v); ok {
			return n, nil
		}
	case FieldFloat:
		if f, ok := number(v); ok {
			return f, nil
		}
	case FieldBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case FieldTime:
		s, ok := v.(string)
		if !ok {
			return nil, mismatch
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
	}
	return nil, mismatch
}

// safeInt reads a whole number within ±maxSafeInt from a json.Number or a float64.
func safeInt(v any) (int64, bool) {
	var n int64
	switch x := v.(type) {
	case json.Number:
		parsed, err := x.Int64()
		if err != nil {
			return 0, false
		}
		n = parsed
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > maxSafeInt {
			return 0, false
		}
		n = int64(x)
	default:
		return 0, false
	}
	if n > maxSafeInt || n < -maxSafeInt {
		return 0, false
	}
	return n, true
}

// number reads a finite float64 from a json.Number or a float64.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, !math.IsInf(x, 0) && !math.IsNaN(x)
	default:
		return 0, false
	}
}
