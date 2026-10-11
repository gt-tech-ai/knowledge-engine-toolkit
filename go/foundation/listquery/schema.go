package listquery

// The domain-agnostic list-query schema descriptor types. These hand-written types are
// the stable contract that the GENERATED per-resource accessors populate — but the
// generated, business-specific code (`<Resource>Schema()`, `AllSchemas()`, …) lives in
// the gen/go/listquery tier (package listqueryschema), NOT here, so foundation stays free
// of resource/domain names. The index-obligation gate, the runtime filter-schema endpoint
// read these types. FilterMap/order.Set (filter.go/order.go) remain the runtime
// SQL-safety boundary; ResourceSchema is the richer descriptor carrying the axes those
// two don't (index_kind, value_source, access_sensitivity).

// ResourceSchema is the generated list-query schema for one resource.
type ResourceSchema struct {
	// Resource is the resource's Go/message name (e.g. "Document").
	Resource string
	// Fields are the resource's filterable/sortable fields, ordered by field name.
	Fields []FieldSchema
}

// KeysetColumn returns the storage column of the resource's keyset-ordering field — the
// primary time tiebreaker CompileOrder appends to every ORDER BY for a stable total order
// (created_at for most resources, joined_at for the membership join tables). It falls
// back to created_at when no field is marked keysettable, matching the Timestamps-mixin
// default. The keysettable annotation is the single source of truth, so a store never
// hard-codes its tiebreaker column.
func (s ResourceSchema) KeysetColumn() string {
	// Index rather than range-by-value: FieldSchema is a wide struct, so a value copy per
	// iteration is wasteful (gocritic rangeValCopy).
	for i := range s.Fields {
		if s.Fields[i].Keysettable {
			return s.Fields[i].Column
		}
	}
	return tiebreakerCreatedAt
}

// TimeColumns returns the set of storage columns whose field type is time (FieldTime).
// Keyset pagination carries these across the opaque base64/JSON token boundary as a
// unix-microsecond int64 — JSON has no native time type — and restores them to time.Time
// before compiling the seek predicate. Driving the round-trip off this generated set ('s
// single source) means a store never hand-lists which of its keyset columns are
// timestamps.
func (s ResourceSchema) TimeColumns() map[string]bool {
	out := make(map[string]bool)
	for i := range s.Fields {
		if s.Fields[i].Type == FieldTime {
			out[s.Fields[i].Column] = true
		}
	}
	return out
}

// Field returns the schema for the named field and whether the resource declares it.
// It is the lookup the value-suggestion handler uses to resolve a requested field to its
// backing column + value_source (rejecting an unknown or non-suggestable field), so the
// suggestable surface is the proto-declared schema's, never the caller's.
func (s ResourceSchema) Field(name string) (FieldSchema, bool) {
	// Index rather than range-by-value: FieldSchema is a wide struct (gocritic
	// rangeValCopy), so copy only the matched element.
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return s.Fields[i], true
		}
	}
	return FieldSchema{}, false
}

// FieldSchema is the full generated schema for one filterable/sortable field —
// every axis declared on the proto.
type FieldSchema struct {
	// ValueSource is the decoded value-provisioning strategy for the field.
	ValueSource ValueSource
	// Join, when non-nil, marks this field as filtering a JOINED table's column; the
	// index-obligation gate reads Join.Table + Join.Columns so it checks the TARGET
	// table, not the resource's own. Populated from schema.json's `join` descriptor; nil
	// for a same-table field.
	Join *JoinTarget
	// Name is the DTO/proto field name — the filter/sort key clients use.
	Name string
	// AccessSensitivity is the field's declared access-sensitivity classification.
	AccessSensitivity string
	// Column is the backing storage column name.
	Column string
	// IndexKind is the declared index kind backing this field (checked by the
	// index-obligation gate); empty when the field declares no index.
	IndexKind string
	// Operators are the filter operators permitted on this field.
	Operators []string
	// Type is the field's value type (string, int, time, id, …).
	Type FieldType
	// CaseInsensitive reports whether string comparisons on this field are case-insensitive.
	CaseInsensitive bool
	// Sortable reports whether the field may be used in an ORDER BY.
	Sortable bool
	// Keysettable reports whether the field is the keyset-ordering (pagination
	// tiebreaker) column.
	Keysettable bool
}

// ValueSource is the decoded value-provisioning strategy for a field.
type ValueSource struct {
	// Kind is one of "enum", "suggest", "entityRef", "freetext".
	Kind string
	// EnumType is the fully-qualified proto enum type when Kind == "enum".
	EnumType string
	// EntityRef is the dimension name when Kind == "entityRef".
	EntityRef string
}
