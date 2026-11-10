package decoration

import (
	"reflect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// dottedOverrides re-reads the overrides of each tier in over by name, finding them in
// section (the raw decoration section). Components are named with dots ("outbox.sqs"),
// which the loader's decode splits into nested maps, so the plain decode sees an override
// of the first segment ("outbox") holding no layers. Each override is read again under
// its full name instead — whether written as one dotted key or nested by segment — and a
// segment that only nests others is no override.
func dottedOverrides(loader interfaces.ConfigLoader, section any, over *Config) error {
	tiers := stdx.As[map[string]any](section)
	cfg := reflect.ValueOf(over).Elem()

	for index := range cfg.NumField() {
		name := cfg.Type().Field(index).Tag.Get("mapstructure")
		tier := stdx.As[map[string]any](tiers[name])
		raw, found := tier["overrides"].(map[string]any)

		if !found {
			continue
		}

		key := SectionKey + "." + name + ".overrides"
		currentTier, found := reflect.TypeAssert[*Tier](cfg.Field(index).Addr())

		if !found {
			return apperr.New(apperr.CodeInternal, "decoration: "+name+" is not a tier")
		}

		currentTier.Overrides = make(map[string]Layers, len(raw))

		err := currentTier.readOverrides(loader, key, "", raw)
		if err != nil {
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
	for text, v := range m {
		inner, ok := v.(map[string]any)
		if !ok || (prefix != "" && isLayerKey(text)) {
			continue
		}

		err := t.readOverride(loader, key, joinDotted(prefix, text), inner)
		if err != nil {
			return err
		}
	}

	return nil
}

// joinDotted appends k to a dotted prefix, which may be empty.
func joinDotted(prefix, k string) string {
	if prefix == "" {
		return k
	}

	return prefix + "." + k
}

// readOverride loads name's layers when it has any, then walks what is nested under it.
func (t *Tier) readOverride(
	loader interfaces.ConfigLoader, key, name string, inner map[string]any,
) error {
	if hasLayer(inner) {
		var layers Layers

		err := loader.UnmarshalKey(key+"."+name, &layers)
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load decoration override "+name,
			)
		}

		t.Overrides[name] = layers
	}

	return t.readOverrides(loader, key, name, inner)
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
	for field := range t.Fields() {
		if field.Tag.Get("mapstructure") == k {
			return true
		}
	}

	return false
}
