package decoration

import (
	"reflect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// dottedOverrides re-reads the overrides of each tier in over by name, finding them in
// section (the raw decoration section). Components are
// named with dots ("outbox.sqs"), which the loader's decode splits into nested maps, so
// the plain decode sees an override of the first segment ("outbox") holding no layers.
// Each override is read again under its full name instead — whether written as one
// dotted key or nested by segment — and a segment that only nests others is no
// override.
func dottedOverrides(loader interfaces.ConfigLoader, section any, over *Config) error {
	tiers, _ := section.(map[string]any)
	cfg := reflect.ValueOf(over).Elem()
	for i := range cfg.NumField() {
		name := cfg.Type().Field(i).Tag.Get("mapstructure")
		tier, _ := tiers[name].(map[string]any)
		raw, ok := tier["overrides"].(map[string]any)
		if !ok {
			continue
		}
		key := SectionKey + "." + name + ".overrides"
		t := cfg.Field(i).Addr().Interface().(*Tier)
		t.Overrides = make(map[string]Layers, len(raw))
		if err := t.readOverrides(loader, key, "", raw); err != nil {
			return err
		}
	}
	return nil
}

// readOverrides reads every override in m (the overrides beneath prefix, read at key)
// under its full name, recursing into the segments that nest deeper names.
func (t *Tier) readOverrides(
	loader interfaces.ConfigLoader,
	key, prefix string,
	m map[string]any,
) error {
	for k, v := range m {
		inner, ok := v.(map[string]any)
		if !ok || (prefix != "" && isLayerKey(k)) {
			continue
		}
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		if hasLayer(inner) {
			var l Layers
			if err := loader.UnmarshalKey(key+"."+name, &l); err != nil {
				return apperr.Wrap(
					err,
					apperr.CodeInvalidInput,
					"load decoration override "+name,
				)
			}
			t.Overrides[name] = l
		}
		if err := t.readOverrides(loader, key, name, inner); err != nil {
			return err
		}
	}
	return nil
}

// hasLayer reports whether m sets any layer, so it is an override rather than only a
// segment of deeper names.
func hasLayer(m map[string]any) bool {
	for k := range m {
		if isLayerKey(k) {
			return true
		}
	}
	return false
}

// isLayerKey reports whether k is the config key of one of Layers' fields.
func isLayerKey(k string) bool {
	t := reflect.TypeFor[Layers]()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("mapstructure") == k {
			return true
		}
	}
	return false
}
