package listquery

import (
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// JoinTarget is a re-export of core/types.JoinTarget, so the generated listqueryschema
// tier can construct a field's join with only the foundation/listquery import (no direct
// core/types import).
type JoinTarget = types.JoinTarget

// FieldType is the value type of an allow-listed filter field. It gates which JSON
// values (and slice element types for in/not_in) are accepted for the field.
type FieldType int

const (
	// FieldString is a text field (eq/ne/like/in/not_in).
	FieldString FieldType = iota
	// FieldInt is an integer field.
	FieldInt
	// FieldFloat is a floating-point field.
	FieldFloat
	// FieldBool is a boolean field (eq/ne).
	FieldBool
	// FieldTime is a timestamp field (RFC3339 in the wire JSON).
	FieldTime
	// FieldID is a UUID/identifier field (compared as a string).
	FieldID
)

// String returns the lowercase token for the field type (string/int/float/bool/time/id),
// matching the (listquery.field) proto type tokens — used when serializing the schema.
func (t FieldType) String() string {
	switch t {
	case FieldString:
		return "string"
	case FieldInt:
		return "int"
	case FieldFloat:
		return "float"
	case FieldBool:
		return "bool"
	case FieldTime:
		return "time"
	case FieldID:
		return "id"
	default:
		return ""
	}
}

// FieldRole is how an analytics query may use a field: grouped by (dimension),
// aggregated (measure), or bucketed by grain (time). Plain list-query fields are
// dimensions (the zero value).
type FieldRole int

const (
	// RoleDimension is a field grouped by and filtered on (the default).
	RoleDimension FieldRole = iota
	// RoleMeasure is a numeric field aggregated by one of its permitted Aggregates.
	RoleMeasure
	// RoleTime is the time field bucketed by a query's grain.
	RoleTime
)

// Field is one allow-listed filterable field: the DTO Name a client may filter on, the
// storage Column it maps to (defaults to Name), and its value Type. A field whose Join is
// set filters a JOINED table's column via a correlated EXISTS rather than the same-table
// Column.
type Field struct {
	// Join, when non-nil, resolves this field to a correlated-EXISTS predicate on a
	// joined table instead of the same-table Column. Filter-only.
	Join *types.JoinTarget
	// Name is the DTO field name a client uses in the filter JSON.
	Name string
	// Column is the storage column the filter compiles against (defaults to Name).
	Column string
	// Ordinal, when non-empty, is the Column's DB values in ascending sort order: sorting
	// the field orders by this value ordinal (a CASE) instead of lexically. Sort-only
	// (does not affect filtering).
	Ordinal []string
	// Aggregates are the aggregations an analytics query may apply to a RoleMeasure field.
	Aggregates []types.Aggregate
	// Type is the field's value type, gating value/element compatibility.
	Type FieldType
	// Role is how an analytics query may use the field (dimension by default).
	Role FieldRole
}

// AsMeasure returns a copy of the field declared as an analytics measure that
// permits exactly the given aggregates.
func (f Field) AsMeasure(aggs ...types.Aggregate) Field {
	f.Role = RoleMeasure
	f.Aggregates = aggs
	return f
}

// AsTime returns a copy of the field declared as the analytics time field.
func (f Field) AsTime() Field {
	f.Role = RoleTime
	return f
}

// Allows reports whether agg is one of the field's permitted aggregates.
func (f Field) Allows(agg types.Aggregate) bool {
	return slices.Contains(f.Aggregates, agg)
}

// WithColumn returns a copy of the field whose storage column differs from its DTO Name
// (for resources where the wire field name is not the Ent column).
func (f Field) WithColumn(column string) Field {
	f.Column = column
	return f
}

// WithJoin returns a copy of the field that filters a JOINED table's column via a
// correlated EXISTS — set by the generator from a `(listquery.field).join` annotation.
func (f Field) WithJoin(join types.JoinTarget) Field {
	f.Join = &join
	return f
}

// WithOrdinal returns a copy of the field that SORTS by a value ordinal — the Column's DB
// values in ascending order — instead of lexically; set by the generator from a
// `(listquery.field).column.sort_ordinal` annotation. Sort-only.
func (f Field) WithOrdinal(values []string) Field {
	f.Ordinal = values
	return f
}

// String declares an allow-listed string field named name (Column defaults to name).
func String(
	name string,
) Field {
	return Field{Name: name, Column: name, Type: FieldString}
}

// Int declares an allow-listed integer field named name.
func Int(name string) Field { return Field{Name: name, Column: name, Type: FieldInt} }

// Float declares an allow-listed floating-point field named name.
func Float(name string) Field { return Field{Name: name, Column: name, Type: FieldFloat} }

// Bool declares an allow-listed boolean field named name.
func Bool(name string) Field { return Field{Name: name, Column: name, Type: FieldBool} }

// Time declares an allow-listed timestamp field named name (RFC3339 on the wire).
func Time(name string) Field { return Field{Name: name, Column: name, Type: FieldTime} }

// ID declares an allow-listed identifier (UUID) field named name.
func ID(name string) Field { return Field{Name: name, Column: name, Type: FieldID} }

// Map is a per-resource allow-list of filterable fields, keyed by DTO Name. Only fields
// added to the Map may be filtered; a filter on any other field is rejected — the
// SQL-safety boundary. Build it fluently: NewMap().Add(String("name"), ID("owner_id"),
// …).
type Map struct {
	// fields is the allow-list of filterable fields, keyed by DTO name.
	fields map[string]Field
}

// NewMap returns an empty filter allow-list.
func NewMap() *Map { return &Map{fields: make(map[string]Field)} }

// Add registers one or more allow-listed fields and returns the Map for chaining.
func (m *Map) Add(fields ...Field) *Map {
	for _, f := range fields {
		m.fields[f.Name] = f
	}
	return m
}

// Lookup returns the allow-listed field for a DTO name and whether it is allow-listed.
func (m *Map) Lookup(name string) (Field, bool) {
	f, ok := m.fields[name]
	return f, ok
}

// Column returns the storage column an allow-listed DTO field maps to, or the field name
// itself when it is not allow-listed — a safe fallback, since the compiler only ever
// receives fields the parser already validated against this Map. It adapts a Map into the
// fold's ColumnFn: Compile(backend, m.Column, filter).
func (m *Map) Column(field string) string {
	if f, ok := m.fields[field]; ok {
		return f.Column
	}
	return field
}

// Join returns the joined-table target for an allow-listed field that filters a joined
// column or nil for a same-table field (or an unknown field). It adapts a Map into the
// join resolver ResolveFilter uses to stamp a clause's JoinTarget:
// ResolveFilter(m.Column, m.Join, filter).
func (m *Map) Join(field string) *types.JoinTarget {
	if f, ok := m.fields[field]; ok {
		return f.Join
	}
	return nil
}

// Ordinal returns the value-ordinal sort order for an allow-listed field, or nil for a
// field with no ordinal (or an unknown field). It adapts a Map into the OrdinalFn the
// ORDER BY path uses: KeysetColumns(m.Column, m.Join, m.Ordinal, sort, tiebreaker).
func (m *Map) Ordinal(field string) []string {
	if f, ok := m.fields[field]; ok {
		return f.Ordinal
	}
	return nil
}
