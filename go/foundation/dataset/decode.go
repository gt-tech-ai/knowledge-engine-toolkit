package dataset

import (
	"bytes"
	"maps"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Decode decodes every section of d into the matching field of the struct out points
// to. A section matches the exported field whose yaml tag name equals its key (an
// untagged field matches its lower-cased name). out must be a non-nil pointer to a
// struct; a key with no field, or a section that does not fit its field (a field the
// type does not declare included), is CodeInvalidInput naming the section's file. A
// field with no section keeps its zero value.
func (d *Dataset) Decode(out any) error {
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return apperr.New(
			apperr.CodeInvalidInput,
			"dataset decode needs a non-nil pointer to a struct",
		)
	}
	fields := fieldsByKey(v.Elem().Type())
	for _, key := range slices.Sorted(maps.Keys(d.sections)) {
		idx, ok := fields[key]
		if !ok {
			return apperr.New(apperr.CodeInvalidInput,
				"data key "+`"`+key+`"`+" in "+d.sourceOf(key)+" has no field")
		}
		if err := d.decodeInto(key, v.Elem().Field(idx).Addr().Interface()); err != nil {
			return err
		}
	}
	return nil
}

// decodeInto strictly decodes the section key into dst, a pointer to its field.
func (d *Dataset) decodeInto(key string, dst any) error {
	raw, err := yaml.Marshal(d.sections[key])
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "re-encode section "+key)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput,
			"decode section "+key+" from "+d.sourceOf(key))
	}
	return nil
}

// fieldsByKey maps each exported field of t to its section key: the yaml tag name, or
// the lower-cased field name when untagged. A field tagged "-" is skipped.
func fieldsByKey(t reflect.Type) map[string]int {
	out := map[string]int{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		out[name] = i
	}
	return out
}
