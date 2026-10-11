package listquery

import (
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ParseSort parses the scalar `sort` wire spec — a comma-separated list of `field[:dir]`
// tokens (e.g. "created_at:desc,name"; dir is asc or desc in either case, asc by default)
// — into an ordered []types.OrderField, against the resource allow-list m. It is the
// SQL-safety boundary for sorting, and it is strict: an unknown or unsortable field (see
// Field.Sortable), a bad direction, an empty token or a repeated field is a
// CodeInvalidInput error rather than a silently dropped token. An empty or blank spec
// yields nil: the deterministic default order then comes from CompileOrder's keyset
// tiebreaker (resource-appropriate — created_at, joined_at, …), not a fixed column the
// resource may not have. Client input is never string-concatenated into SQL.
func ParseSort(m *Map, spec string) ([]types.OrderField, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var out []types.OrderField
	seen := make(map[string]bool)
	for tok := range strings.SplitSeq(spec, ",") {
		of, err := parseSortToken(m, tok)
		if err != nil {
			return nil, err
		}
		if seen[of.Field] {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"sort: field repeated: "+of.Field,
			)
		}
		seen[of.Field] = true
		out = append(out, of)
	}
	return out, nil
}

// parseSortToken parses one `field[:dir]` token against the sortable allow-list.
func parseSortToken(m *Map, tok string) (types.OrderField, error) {
	field, dir, hasDir := strings.Cut(tok, ":")
	field = strings.TrimSpace(field)
	if field == "" {
		return types.OrderField{}, apperr.New(
			apperr.CodeInvalidInput,
			"sort: empty field",
		)
	}
	if !m.Sortable(field) {
		return types.OrderField{}, apperr.New(
			apperr.CodeInvalidInput,
			"sort: field not sortable: "+field,
		)
	}
	of := types.OrderField{Field: field}
	if !hasDir {
		return of, nil
	}
	switch dir = strings.TrimSpace(dir); {
	case strings.EqualFold(dir, "asc"):
	case strings.EqualFold(dir, "desc"):
		of.Desc = true
	default:
		return types.OrderField{}, apperr.New(
			apperr.CodeInvalidInput,
			"sort: direction must be asc or desc: "+dir,
		)
	}
	return of, nil
}
