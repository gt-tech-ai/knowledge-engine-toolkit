package listquery

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// maxSafeInt is the largest integer every client can carry exactly (2^53 - 1, the
// largest integer a float64 or a JavaScript number represents without rounding).
const maxSafeInt = 1<<53 - 1

// Parse compiles a JSON filter string, gated by the resource allow-list m, into the core
// Filter contract: it decodes the string into a types.FilterNode and parses that with
// ParseFilter. Blank input (or "{}") yields an empty CompositeFilter (IsEmpty() true) —
// a store applies no WHERE. Malformed JSON, or anything ParseFilter rejects, is a
// CodeInvalidInput error (ARCHITECTURE.md#error-codes).
func Parse(fieldMap *Map, jsonStr string) (types.Filter, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return stdx.Zero[types.CompositeFilter](), nil // no filter → IsEmpty
	}

	var node types.FilterNode

	err := json.Unmarshal([]byte(jsonStr), &node)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeInvalidInput),
			"filter: invalid JSON",
		)
	}

	return ParseFilter(fieldMap, node)
}

// ParseFilter checks a filter tree against the resource allow-list m and builds the core
// Filter contract from it. The zero node yields an empty CompositeFilter. A node with
// other than one arm, an empty $and/$or, nesting past types.MaxFilterDepth, an empty or
// unknown operator, a non-allow-listed field, or a value that does not fit the field's
// type (in/not_in take a []any) is a CodeInvalidInput error. The result is a
// FilterClause for a single comparison and a CompositeFilter for $and/$or.
func ParseFilter(m *Map, n types.FilterNode) (types.Filter, error) {
	if n.Clause == nil && n.And == nil && n.Or == nil {
		return stdx.Zero[types.CompositeFilter](), nil // no filter → IsEmpty
	}

	return parseNode(m, n, 0)
}

// parseNode walks one node (exactly one arm) into a Filter, bounding recursion at
// types.MaxFilterDepth.
func parseNode(
	fieldMap *Map,
	filterNode types.FilterNode,
	depth int,
) (types.Filter, error) {
	if depth > types.MaxFilterDepth {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: nesting exceeds max depth",
		)
	}

	arms := 0

	for _, set := range []bool{
		filterNode.Clause != nil, filterNode.And != nil,
		filterNode.Or != nil,
	} {
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
	case filterNode.Clause != nil:
		return parseClause(fieldMap, *filterNode.Clause)
	case filterNode.And != nil:
		return parseComposite(fieldMap, "$and", filterNode.And, depth)
	default:
		return parseComposite(fieldMap, "$or", filterNode.Or, depth)
	}
}

// parseComposite walks the members of a $and/$or group into a CompositeFilter.
func parseComposite(
	fieldMap *Map,
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
		f, err := parseNode(fieldMap, member, depth+1)
		if err != nil {
			return nil, err
		}

		subs = append(subs, f)
	}

	if key == "$and" {
		return types.CompositeFilter{And: subs, Or: nil}, nil
	}

	return types.CompositeFilter{Or: subs, And: nil}, nil
}

// parseClause checks one comparison: the operator against the known set, the field
// against the allow-list and the value against the field's type.
func parseClause(
	fieldMap *Map,
	filterNodeClause types.FilterNodeClause,
) (types.Filter, error) {
	if !filterNodeClause.Op.Known() {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: unknown operator "+string(filterNodeClause.Op),
		)
	}

	field, ok := fieldMap.Lookup(filterNodeClause.Field)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"filter: field not allow-listed: "+filterNodeClause.Field,
		)
	}

	value, err := decodeValue(filterNodeClause.Op, filterNodeClause.Value, field.Type)
	if err != nil {
		return nil, err
	}

	return types.FilterClause{
		Field:    field.Name,
		Operator: filterNodeClause.Op,
		Value:    value,
		Join:     nil,
	}, nil
}

// decodeValue checks an operator operand: for in/not_in a []any of type-compatible
// elements (a structured source converts a typed slice first; an empty array is valid,
// a matches-nothing clause), otherwise a scalar.
func decodeValue(op types.FilterOperator, v any, fieldType FieldType) (any, error) {
	if op != types.OpIn && op != types.OpNotIn {
		return decodeScalar(v, fieldType)
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
		d, err := decodeScalar(el, fieldType)
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
func decodeScalar(value any, ft FieldType) (any, error) {
	switch ft {
	case FieldString, FieldID:
		return scalarFrom(value, assertScalar[string])
	case FieldInt:
		return scalarFrom(value, safeInt)
	case FieldFloat:
		return scalarFrom(value, number)
	case FieldBool:
		return scalarFrom(value, assertScalar[bool])
	case FieldTime:
		return decodeTimeScalar(value)
	}

	return nil, scalarMismatch()
}

// scalarMismatch is the error for a value that does not fit its field's type.
func scalarMismatch() error {
	return apperr.New(
		apperr.CodeInvalidInput,
		"filter: value type does not match field type",
	)
}

// assertScalar converts v by type assertion.
func assertScalar[T any](v any) (T, bool) {
	scalar, ok := v.(T)

	return scalar, ok
}

// scalarFrom returns v converted by convert, or the mismatch error when it does not fit.
func scalarFrom[T any](v any, convert func(any) (T, bool)) (any, error) {
	scalar, ok := convert(v)
	if !ok {
		return nil, scalarMismatch()
	}

	return scalar, nil
}

// decodeTimeScalar parses an RFC3339 string.
func decodeTimeScalar(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, scalarMismatch()
	}

	instant, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"filter: time value must be RFC3339",
		)
	}

	return instant, nil
}

// safeInt reads a whole number within ±maxSafeInt from a json.Number or a float64.
func safeInt(v any) (int64, bool) {
	n, ok := wholeNumber(v)
	if !ok || n > maxSafeInt || n < -maxSafeInt {
		return 0, false
	}

	return n, true
}

// wholeNumber converts a JSON number with no fractional part.
func wholeNumber(v any) (int64, bool) {
	switch secondNumber := v.(type) {
	case json.Number:
		parsed, err := secondNumber.Int64()

		return parsed, err == nil
	case float64:
		return int64(
				secondNumber,
			), secondNumber == math.Trunc(secondNumber) &&
				math.Abs(secondNumber) <= maxSafeInt
	default:
		return 0, false
	}
}

// number reads a finite float64 from a json.Number or a float64.
func number(v any) (float64, bool) {
	switch amount := v.(type) {
	case json.Number:
		f, err := amount.Float64()

		return f, err == nil
	case float64:
		return amount, !math.IsInf(amount, 0) && !math.IsNaN(amount)
	default:
		return 0, false
	}
}
