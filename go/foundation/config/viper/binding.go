package viper

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/viper"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// envReplacer returns the replacer that maps a mapstructure dotted path to the
// <PREFIX>_<PATH> env-var form (dots and dashes → underscores); the loader's
// SetEnvKeyReplacer uses the same one.
func envReplacer() *strings.Replacer {
	return strings.NewReplacer(".", "_", "-", "_")
}

// checkSchema returns a CodeInvalidInput error unless schema is nil, a struct, or a
// pointer to a struct — the only shapes deriveEnvBindings can walk.
func checkSchema(schema any) error {
	if schema == nil {
		return nil
	}
	t := reflect.TypeOf(schema)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return apperr.InvalidInput(fmt.Sprintf(
			"config schema must be a struct or a pointer to one, got %T", schema,
		))
	}
	return nil
}

// deriveEnvBindings binds every leaf field of cfg to its env vars, derived from
// the field's mapstructure path: the canonical <envPrefix>_<PATH> name (plain
// <PATH> when envPrefix is empty) plus any aliases listed in an `envalias:"A,B"`
// tag (flat names like DB_HOST). Adding a field auto-binds its env var, and an
// alias is a tag, not a code edit. Leaves whose path is a key of skip are left
// unbound (the caller binds them itself). cfg must pass checkSchema.
func deriveEnvBindings(
	v *viper.Viper,
	cfg any,
	envPrefix string,
	skip map[string][]string,
) {
	skipped := make(map[string]bool, len(skip))
	for key := range skip {
		skipped[strings.ToLower(key)] = true
	}
	walkEnvBindings(v, reflect.TypeOf(cfg), "", envPrefix, skipped)
}

// walkEnvBindings recurses t's struct fields, accumulating the mapstructure
// path prefix, and binds each scalar leaf. Nested structs recurse; a ",squash"
// struct recurses without adding a path segment; scalars (including time.Duration,
// whose Kind is Int64) are leaves.
func walkEnvBindings(
	v *viper.Viper, t reflect.Type, prefix, envPrefix string, skipped map[string]bool,
) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		name, squash, ok := fieldKey(f)
		if !ok {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if squash && ft.Kind() == reflect.Struct {
			walkEnvBindings(v, ft, prefix, envPrefix, skipped)
			continue
		}
		path := strings.ToLower(name)
		if prefix != "" {
			path = prefix + "." + path
		}
		if ft.Kind() == reflect.Struct {
			walkEnvBindings(v, ft, path, envPrefix, skipped)
			continue
		}
		if skipped[path] {
			continue
		}
		bindLeaf(v, path, envPrefix, f.Tag.Get("envalias"))
	}
}

// fieldKey reads a field's key the way mapstructure does: the tag name before the
// first comma, falling back to the field name when the tag or its name is empty.
// ok is false for fields mapstructure never decodes (unexported, "-", ",remain");
// squash reports a ",squash" option.
func fieldKey(f reflect.StructField) (name string, squash, ok bool) {
	if !f.IsExported() {
		return "", false, false
	}
	name, opts, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
	if name == "-" {
		return "", false, false
	}
	for opt := range strings.SplitSeq(opts, ",") {
		switch strings.TrimSpace(opt) {
		case "squash":
			squash = true
		case "remain":
			return "", false, false
		}
	}
	if name == "" {
		name = f.Name
	}
	return name, squash, true
}

// bindLeaf binds path to the canonical <envPrefix>_<PATH> env var plus the
// comma-separated aliases. Passing explicit names disables Viper's auto-prefix for
// the binding, so the prefixed name is listed explicitly.
func bindLeaf(v *viper.Viper, path, envPrefix, aliases string) {
	envName := strings.ToUpper(envReplacer().Replace(path))
	if envPrefix != "" {
		envName = strings.ToUpper(envPrefix) + "_" + envName
	}
	names := []string{path, envName}
	if aliases != "" {
		names = append(names, strings.Split(aliases, ",")...)
	}
	_ = v.BindEnv(names...)
}
