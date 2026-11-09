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
	callback func(old string) (string, bool),
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

	currentScalarRewrite := &scalarRewrite{
		lines:   splitLines(src),
		parents: parentsOf(root(docs[0]), nil, map[*yaml.Node]*yaml.Node{}),
		top:     root(docs[0]),
		path:    path,
		fn:      callback,
	}
	out := src

	for _, m := range matches {
		out, err = currentScalarRewrite.apply(m, out)
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
func (r *scalarRewrite) apply(match Match, out []byte) ([]byte, error) {
	if match.Node.Kind != yaml.ScalarNode {
		return out, nil
	}

	value, found := r.fn(match.Node.Value)
	if !found {
		return out, nil
	}

	replacement := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	if match.Node.Style == yaml.DoubleQuotedStyle {
		replacement.Style = yaml.DoubleQuotedStyle
	}

	parent := r.parents[match.Node]
	if parent == nil {
		parent = r.top
	}

	spliced, found := replaceScalar(r.lines, parent, match.Node, replacement)
	if !found {
		return nil, invalid(r.path, "a scalar at line "+strconv.Itoa(match.Node.Line)+
			" cannot be rewritten in place")
	}

	return spliced, nil
}

// parentsOf maps every node under n to its parent (n's own parent is parent).
func parentsOf(
	node, parent *yaml.Node,
	acc map[*yaml.Node]*yaml.Node,
) map[*yaml.Node]*yaml.Node {
	if node == nil {
		return acc
	}

	acc[node] = parent
	for _, c := range node.Content {
		parentsOf(c, node, acc)
	}

	return acc
}
