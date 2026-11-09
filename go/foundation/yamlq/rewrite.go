package yamlq

import (
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Rewrite replaces every scalar the query path selects in the first document of src
// with fn's value, splicing each new token into its own line so the rest of the file
// (comments, indentation, key order, the other scalars) is untouched; a double-quoted
// token stays double-quoted. The path may use every query step (`..` and `[*]`
// included), so a value is rewritten wherever it sits. fn returning false leaves that
// scalar unchanged; a scalar the splice cannot rewrite in place (a block scalar, a flow
// collection's item, a wrapped scalar) is CodeInvalidInput.
func Rewrite(
	src []byte,
	path string,
	fn func(old string) (string, bool),
) ([]byte, error) {
	docs, err := DecodeAll(src)
	if err != nil {
		return nil, err
	}

	if len(docs) == 0 {
		return src, nil
	}

	matches, err := Path(docs[0], path)
	if err != nil {
		return nil, err
	}

	rw := &scalarRewrite{
		lines:   splitLines(src),
		parents: parentsOf(root(docs[0]), nil, map[*yaml.Node]*yaml.Node{}),
		top:     root(docs[0]),
		path:    path,
		fn:      fn,
	}
	out := src

	for _, m := range matches {
		out, err = rw.apply(m, out)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// scalarRewrite is the state one Rewrite shares across its matches.
type scalarRewrite struct {
	// parents maps every node of the document to its parent.
	parents map[*yaml.Node]*yaml.Node

	// top is the document's content node, the parent of a parentless match.
	top *yaml.Node

	// fn supplies each new value, returning false to keep the old one.
	fn func(old string) (string, bool)

	// path is the query, named in errors.
	path string

	// lines is the source, updated in place by each splice.
	lines []string
}

// apply rewrites one match, returning the file so far: out when m is left alone.
func (r *scalarRewrite) apply(m Match, out []byte) ([]byte, error) {
	if m.Node.Kind != yaml.ScalarNode {
		return out, nil
	}

	value, ok := r.fn(m.Node.Value)
	if !ok {
		return out, nil
	}

	replacement := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	if m.Node.Style == yaml.DoubleQuotedStyle {
		replacement.Style = yaml.DoubleQuotedStyle
	}

	parent := r.parents[m.Node]
	if parent == nil {
		parent = r.top
	}

	spliced, ok := replaceScalar(r.lines, parent, m.Node, replacement)
	if !ok {
		return nil, invalid(r.path, "a scalar at line "+strconv.Itoa(m.Node.Line)+
			" cannot be rewritten in place")
	}

	return spliced, nil
}

// parentsOf maps every node under n to its parent (n's own parent is parent).
func parentsOf(
	n, parent *yaml.Node,
	acc map[*yaml.Node]*yaml.Node,
) map[*yaml.Node]*yaml.Node {
	if n == nil {
		return acc
	}

	acc[n] = parent
	for _, c := range n.Content {
		parentsOf(c, n, acc)
	}

	return acc
}
