package codebase

import (
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// decodeTOML decodes a TOML file into maps and lists. An offset date-time is a
// time.Time; a local date or local date-time is normalized to one in UTC and a local
// time to its text, so an expression compares every date as a timestamp.
func decodeTOML(src []byte, p string) (any, error) {
	var doc map[string]any

	err := toml.Unmarshal(src, &doc)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "toml "+p)
	}

	return normalizeTOML(doc), nil
}

// normalizeTOML replaces go-toml's local date and time values in v, recursively, with
// the forms decodeTOML documents.
func normalizeTOML(value any) any {
	switch items := value.(type) {
	case map[string]any:
		for k, e := range items {
			items[k] = normalizeTOML(e)
		}

		return items
	case []any:
		for i, e := range items {
			items[i] = normalizeTOML(e)
		}

		return items
	case toml.LocalDate:
		return items.AsTime(time.UTC)
	case toml.LocalDateTime:
		return items.AsTime(time.UTC)
	case toml.LocalTime:
		return items.String()
	default:
		return value
	}
}
