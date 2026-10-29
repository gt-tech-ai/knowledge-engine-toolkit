package codebase

import (
	"time"

	toml "github.com/pelletier/go-toml/v2"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// decodeTOML decodes a TOML file into maps and lists. An offset date-time is a
// time.Time; a local date or local date-time is normalized to one in UTC and a local
// time to its text, so an expression compares every date as a timestamp.
func decodeTOML(src []byte, p string) (any, error) {
	var doc map[string]any
	if err := toml.Unmarshal(src, &doc); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "toml "+p)
	}
	return normalizeTOML(doc), nil
}

// normalizeTOML replaces go-toml's local date and time values in v, recursively, with
// the forms decodeTOML documents.
func normalizeTOML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeTOML(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeTOML(e)
		}
		return t
	case toml.LocalDate:
		return t.AsTime(time.UTC)
	case toml.LocalDateTime:
		return t.AsTime(time.UTC)
	case toml.LocalTime:
		return t.String()
	default:
		return v
	}
}
