package rpc

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// EnumMapper bidirectionally maps a proto enum P to a domain enum D from a single
// forward table, so a value's proto↔domain correspondence is declared once instead
// of being re-encoded in a to-domain switch, a to-proto switch, and a filter switch.
// The reverse (domain→proto) table is derived at construction. It serves the three
// conversion shapes the RPC converters need:
//
//   - ToDomainOrError — validating: a proto value absent from the table is rejected
//     as InvalidInput (used where the proto has no valid unspecified peer, or where
//     an explicit unspecified→domain mapping is a table entry).
//   - ToProtoValue — total: a domain value absent from the reverse table maps to the
//     protoZero fallback (the proto UNSPECIFIED value).
//   - ToDomainFilter — filter: the protoZero (unspecified) value means "no filter"
//     and returns ok=false; any other known value maps through.
//
// P and D are the proto and domain enum types (both comparable — proto enums are
// int32-based, domain enums string-based here).
type EnumMapper[P comparable, D comparable] struct {
	// domainZero is the domain value returned alongside the error from
	// ToDomainOrError, preserving each converter's original error-path return.
	domainZero D

	// protoZero is the proto UNSPECIFIED value: the ToProtoValue fallback for an
	// unmapped domain value, and the "no filter" sentinel for ToDomainFilter.
	protoZero P

	// toDomain is the forward (proto→domain) table.
	toDomain map[P]D

	// toProto is the derived reverse (domain→proto) table.
	toProto map[D]P

	// label names the enum in the InvalidInput message (e.g. "document format").
	label string
}

// NewEnumMapper builds a mapper from the forward (proto→domain) table, deriving the
// reverse table. protoZero is the proto UNSPECIFIED value and domainZero is the
// domain value the validating conversion returns on an unknown proto value.
//
// It panics if two proto values map to the same domain value: the derived reverse
// (domain→proto) table requires unique domain values, and a collision would make
// ToProtoValue a silent last-writer-wins under Go's randomized map iteration. The
// mappers are constructed at package init, so a violation fails loudly at startup.
func NewEnumMapper[P, D comparable](
	label string,
	protoZero P,
	domainZero D,
	toDomain map[P]D,
) EnumMapper[P, D] {
	toProto := make(map[D]P, len(toDomain))
	for p, d := range toDomain {
		if existing, dup := toProto[d]; dup {
			panic(fmt.Sprintf(
				"NewEnumMapper(%s): domain value %v maps from both %v and %v; "+
					"the reverse table requires unique domain values",
				label, d, existing, p,
			))
		}
		toProto[d] = p
	}
	return EnumMapper[P, D]{
		label:      label,
		protoZero:  protoZero,
		domainZero: domainZero,
		toDomain:   toDomain,
		toProto:    toProto,
	}
}

// ToDomainOK returns the mapped domain value and whether the proto value was present
// in the table, without allocating an error — for callers that reject an unknown
// value with their own message rather than the mapper's generic one.
func (m EnumMapper[P, D]) ToDomainOK(p P) (D, bool) {
	d, ok := m.toDomain[p]
	return d, ok
}

// ToDomainOrError returns the mapped domain value, or (domainZero, InvalidInput)
// for a proto value absent from the table.
func (m EnumMapper[P, D]) ToDomainOrError(p P) (D, error) {
	if d, ok := m.ToDomainOK(p); ok {
		return d, nil
	}
	return m.domainZero, apperr.InvalidInput(fmt.Sprintf("unknown %s: %v", m.label, p))
}

// ToProtoValue returns the mapped proto value, or protoZero for a domain value
// absent from the reverse table.
func (m EnumMapper[P, D]) ToProtoValue(d D) P {
	if p, ok := m.toProto[d]; ok {
		return p
	}
	return m.protoZero
}

// ToDomainFilter maps a proto value into a domain value for filtering: the
// unspecified (protoZero) value means "no filter" and returns ok=false, as does any
// value absent from the table. The zero domain value is returned when ok is false.
func (m EnumMapper[P, D]) ToDomainFilter(p P) (D, bool) {
	if p == m.protoZero {
		var zero D
		return zero, false
	}
	return m.ToDomainOK(p)
}

// ToDomainEntries returns the forward (proto→domain) table for derived projections
// (e.g. building a filter-token→value map from the same source of truth). The returned
// map is the mapper's own table — read-only; callers must not mutate it.
func (m EnumMapper[P, D]) ToDomainEntries() map[P]D {
	return m.toDomain
}
