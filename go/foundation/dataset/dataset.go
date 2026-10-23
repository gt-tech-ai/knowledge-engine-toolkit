// Package dataset merge-loads a directory of YAML data files into one set of
// top-level sections and decodes each section strictly into a typed value, so a
// front-end's repository data (tools, paths, checks) can be split across files by
// topic without a key silently overriding another or a misspelled field being dropped.
package dataset

import (
	"bytes"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Dataset is the merged top-level sections of a directory of YAML files.
type Dataset struct {
	// sections maps a top-level key to its YAML node.
	sections map[string]*yaml.Node

	// files maps a top-level key to the file that defined it.
	files map[string]string
}

// Load reads every *.yaml file directly in dir of fsys, in name order, and merges
// their top-level keys. A file that does not parse, or a key defined in two files,
// is CodeInvalidInput naming the file (both files for a duplicate).
func Load(fsys fs.FS, dir string) (*Dataset, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "read data dir "+dir)
	}
	d := &Dataset{sections: map[string]*yaml.Node{}, files: map[string]string{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		// io/fs paths are always slash-separated, so path (not filepath) is correct here.
		name := path.Join(dir, e.Name()) // nosemgrep: use-filepath-join
		if err := d.add(fsys, name); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// add merges the top-level keys of the file at p into d.
func (d *Dataset) add(fsys fs.FS, p string) error {
	raw, err := fs.ReadFile(fsys, p)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read "+p)
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+p)
	}
	for _, key := range slices.Sorted(maps.Keys(doc)) {
		if prev, dup := d.files[key]; dup {
			return apperr.New(apperr.CodeInvalidInput,
				"data key "+`"`+key+`"`+" is defined in both "+prev+" and "+p)
		}
		node := doc[key]
		unfold(&node)
		d.sections[key], d.files[key] = &node, p
	}
	return nil
}

// unfold restyles every folded scalar under n as literal. A section is re-encoded before
// it decodes, and yaml.v3 re-folds a folded scalar with more-indented lines into a
// different string; a literal scalar of the same value encodes back to exactly it.
func unfold(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Style&yaml.FoldedStyle != 0 {
		n.Style = n.Style&^yaml.FoldedStyle | yaml.LiteralStyle
	}
	for _, c := range n.Content {
		unfold(c)
	}
}

// Section decodes the top-level section name into a T, rejecting a field T does not
// declare. A missing section is CodeNotFound; a section that does not fit T is
// CodeInvalidInput naming the section's file.
func (d *Dataset) Section[T any](name string) (T, error) {
	var out T
	node, ok := d.sections[name]
	if !ok {
		return out, apperr.New(apperr.CodeNotFound, "no data section "+`"`+name+`"`)
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return out, apperr.Wrap(err, apperr.CodeInternal, "re-encode section "+name)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		return out, apperr.Wrap(err, apperr.CodeInvalidInput,
			"decode section "+name+" from "+d.files[name])
	}
	return out, nil
}
