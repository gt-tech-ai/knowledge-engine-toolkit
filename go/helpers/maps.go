package helpers

// Typed accessors over an untyped map[string]any (a decoded JSON/YAML/OpenAPI tree).
// Each returns the zero value (nil map/slice, "" string) on a missing key OR a wrong
// type, collapsing the repeated `v, ok := m[k].(T); if !ok {…}` guard ladder to one
// call the caller tests against the zero value.

// GetMap returns m[k] as a map[string]any, or nil if the key is absent or not a map.
func GetMap(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

// GetSlice returns m[k] as a []any, or nil if the key is absent or not a slice.
func GetSlice(m map[string]any, k string) []any {
	v, _ := m[k].([]any)
	return v
}

// GetString returns m[k] as a string, or "" if the key is absent or not a string.
func GetString(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// AsMap asserts an arbitrary value (e.g. a range element) to map[string]any,
// returning nil when it is not a map — the value-based analogue of GetMap.
func AsMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
