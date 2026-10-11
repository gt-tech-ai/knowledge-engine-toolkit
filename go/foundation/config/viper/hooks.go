package viper

import (
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// mapEntrySeparator separates the "key=value" entries of a flat map string, and
// mapKVSeparator separates a single entry's key from its value.
//
// The entry separator is ";" (not ",") deliberately: a value may itself be a
// comma-separated list — e.g. auth.service_tokens carries a {current,previous}
// token pair per caller for a zero-downtime rotation window — so "," is reserved
// for the value and must not double as the entry delimiter.
const (
	mapEntrySeparator = ";" // separates the "key=value" entries of a flat map string
	mapKVSeparator    = "=" // separates a single entry's key from its value
)

// StringToStringMapHookFunc returns a mapstructure decode hook that converts a
// flat "key=value;key=value" string into a map[string]string.
//
// It exists so a config map field can be populated from a single flat env var:
// the deploy path delivers a Kubernetes secretKeyRef, which can only carry a
// scalar, so a nested YAML map (e.g. auth.service_tokens) has no way to arrive
// over env. The hook splits the scalar on ";" into entries, then each entry on
// the first "=" into a key/value; a value keeps any embedded "," (so a caller's
// {current,previous} rotation pair survives intact). Surrounding whitespace is
// trimmed; empty and malformed (no "=") entries are skipped; an empty string
// yields an empty (non-nil) map.
//
// Only string→map[string]string conversions are rewritten; every other decode
// (a YAML-sourced map, a map[string]struct, a slice)
// passes through untouched, so the hook composes safely with the existing chain.
func StringToStringMapHookFunc() mapstructure.DecodeHookFuncType {
	return func(from, to reflect.Type, data any) (any, error) {
		if from.Kind() != reflect.String {
			return data, nil
		}
		if to.Kind() != reflect.Map ||
			to.Key().Kind() != reflect.String ||
			to.Elem().Kind() != reflect.String {
			return data, nil
		}

		raw, _ := data.(string)
		result := make(map[string]string)
		if strings.TrimSpace(raw) == "" {
			return result, nil
		}
		for entry := range strings.SplitSeq(raw, mapEntrySeparator) {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			key, value, ok := strings.Cut(entry, mapKVSeparator)
			if !ok {
				continue
			}
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
		return result, nil
	}
}
