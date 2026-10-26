package procedure

import (
	"fmt"
	"time"
)

// Text returns key's string value; a missing or non-string value is "".
func (v Vars) Text(key string) string {
	s, _ := v[key].(string)
	return s
}

// Bool returns key's boolean value; a missing or non-boolean value is false.
func (v Vars) Bool(key string) bool {
	b, _ := v[key].(bool)
	return b
}

// Strings returns key's string list, accepting the JSON-decoded form a tool call sends
// (a list of any); a missing or other value is nil.
func (v Vars) Strings(key string) []string {
	switch val := v[key].(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

// Int returns key's integer value — an int, the int64 a capture stores or the float64
// JSON decodes to; a missing or other value is 0.
func (v Vars) Int(key string) int {
	switch val := v[key].(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return 0
	}
}

// Duration returns key's duration — the time.Duration a CLI flag carries, or the text a
// tool call sends ("90s"); a missing, other or unparseable value is 0.
func (v Vars) Duration(key string) time.Duration {
	switch val := v[key].(type) {
	case time.Duration:
		return val
	case string:
		d, _ := time.ParseDuration(val)
		return d
	default:
		return 0
	}
}
