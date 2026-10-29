// Package dataset merge-loads YAML data files into one set of top-level sections and
// decodes each section strictly into a typed value, so a front-end's repository data
// (tools, paths, checks) can be split across files by topic. The files are slices of one
// document: maps merge key by key and lists concatenate in file order, while a value set
// twice fails naming both files instead of one silently winning, and a misspelled field
// fails instead of being dropped.
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

// Dataset is the merged top-level sections of a set of YAML files.
type Dataset struct {
	// sections maps a top-level key to its merged YAML node.
	sections map[string]*yaml.Node

	// files maps a top-level key to the files that contributed to it, in load order.
	files map[string][]string

	// origin maps a claimed key path (a scalar's dotted path, or a named list item's
	// `path[name]`) to the file that set it.
	origin map[string]string
}

// Load reads every *.yaml file directly in dir of fsys, in name order, and merges them.
// A file that does not parse, or a value set in two files, is CodeInvalidInput naming
// the file (both files and the key path for a conflict); a missing dir is CodeNotFound.
func Load(fsys fs.FS, dir string) (*Dataset, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "read data dir "+dir)
	}
	d := newDataset()
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

// LoadTree reads every *.yaml file under dir of fsys, its subdirectories included, in
// lexical path order, and merges them as Load does. A subdirectory whose dir-relative
// path is in skip (a template or scaffold tree holding YAML that is not data) is not
// read. A missing dir is CodeNotFound.
func LoadTree(fsys fs.FS, dir string, skip ...string) (*Dataset, error) {
	var files []string
	err := fs.WalkDir(fsys, dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		if e.IsDir() && rel != "" && slices.Contains(skip, rel) {
			return fs.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "read data dir "+dir)
	}
	slices.Sort(files)
	d := newDataset()
	for _, p := range files {
		if err := d.add(fsys, p); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// newDataset returns an empty Dataset.
func newDataset() *Dataset {
	return &Dataset{
		sections: map[string]*yaml.Node{},
		files:    map[string][]string{},
		origin:   map[string]string{},
	}
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
		node := doc[key]
		unfold(&node)
		if prev, ok := d.sections[key]; ok {
			if err := d.merge(prev, &node, key, p); err != nil {
				return err
			}
		} else {
			d.sections[key] = &node
			d.claim(&node, key, p)
		}
		d.files[key] = append(d.files[key], p)
	}
	return nil
}

// merge folds src (from file p) into dst at the key path at: maps merge key by key and
// lists concatenate. A scalar on both sides, two kinds that differ, or a named list item
// already set by another file is CodeInvalidInput naming at and both files.
func (d *Dataset) merge(dst, src *yaml.Node, at, p string) error {
	switch {
	case dst.Kind == yaml.MappingNode && src.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(src.Content); i += 2 {
			k, v := src.Content[i], src.Content[i+1]
			sub := at + "." + k.Value
			if existing := mappingValue(dst, k.Value); existing != nil {
				if err := d.merge(existing, v, sub, p); err != nil {
					return err
				}
				continue
			}
			dst.Content = append(dst.Content, k, v)
			d.claim(v, sub, p)
		}
		return nil
	case dst.Kind == yaml.SequenceNode && src.Kind == yaml.SequenceNode:
		for _, item := range src.Content {
			key := at + "[" + itemName(item) + "]"
			if prev, dup := d.origin[key]; dup && itemName(item) != "" && prev != p {
				return conflict(key, prev, p)
			}
		}
		dst.Content = append(dst.Content, src.Content...)
		d.claim(src, at, p)
		return nil
	default:
		return conflict(at, d.firstFile(at), p)
	}
}

// claim records p as the origin of every scalar under n (by dotted path) and of every
// named item of a list under n (by `path[name]`), so a later file setting one conflicts.
func (d *Dataset) claim(n *yaml.Node, at, p string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			d.claim(n.Content[i+1], at+"."+n.Content[i].Value, p)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			name := itemName(item)
			if name == "" {
				continue
			}
			if _, seen := d.origin[at+"["+name+"]"]; !seen {
				d.origin[at+"["+name+"]"] = p
			}
		}
	case yaml.DocumentNode, yaml.ScalarNode, yaml.AliasNode:
		d.origin[at] = p
	}
}

// firstFile returns the file that set the key path at, or else the first file that
// contributed to its top-level section.
func (d *Dataset) firstFile(at string) string {
	if p, ok := d.origin[at]; ok {
		return p
	}
	top, _, _ := strings.Cut(at, ".")
	if files := d.files[top]; len(files) > 0 {
		return files[0]
	}
	return "?"
}

// mappingValue returns the value node of key in the mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// itemName returns a list item's `name` scalar, or "" when it has none.
func itemName(item *yaml.Node) string {
	if item.Kind != yaml.MappingNode {
		return ""
	}
	if v := mappingValue(item, "name"); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// conflict is the CodeInvalidInput error for the key path at set in both prev and p.
func conflict(at, prev, p string) error {
	return apperr.New(apperr.CodeInvalidInput,
		"data key "+`"`+at+`"`+" is defined in both "+prev+" and "+p)
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
// CodeInvalidInput naming the section's files.
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
			"decode section "+name+" from "+d.sourceOf(name))
	}
	return out, nil
}

// sourceOf names the files that contributed to the section key, comma-separated.
func (d *Dataset) sourceOf(key string) string {
	return strings.Join(d.files[key], ", ")
}
