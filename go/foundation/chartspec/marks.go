package chartspec

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// fieldKind classifies a pane axis for mark inference.
type fieldKind int

const (
	// kindOrdinal is a dimension (or an empty axis).
	kindOrdinal fieldKind = iota
	// kindQuantitative is an aggregated measure.
	kindQuantitative
	// kindTemporal is the time field.
	kindTemporal
)

// kindOf classifies a tuple by its innermost field: an aggregated reference is
// quantitative, the cube's time field temporal, anything else (or no field) ordinal.
func kindOf(t Tuple, fields *listquery.Map) fieldKind {
	if len(t.Fields) == 0 {
		return kindOrdinal
	}
	last := t.Fields[len(t.Fields)-1]
	if last.Agg != types.AggNone {
		return kindQuantitative
	}
	if f, ok := fields.Lookup(last.Name); ok && f.Role == listquery.RoleTime {
		return kindTemporal
	}
	return kindOrdinal
}

// markFor returns the explicit mark when set, else the inference from the two
// axis kinds: Q×Q→point, T×Q→line, O×Q→bar (either orientation), otherwise text.
func markFor(explicit types.Mark, row, col fieldKind) types.Mark {
	if explicit != types.MarkAuto {
		return explicit
	}
	switch {
	case row == kindQuantitative && col == kindQuantitative:
		return types.MarkPoint
	case (row == kindTemporal && col == kindQuantitative) ||
		(row == kindQuantitative && col == kindTemporal):
		return types.MarkLine
	case row == kindQuantitative || col == kindQuantitative:
		return types.MarkBar
	default:
		return types.MarkText
	}
}
