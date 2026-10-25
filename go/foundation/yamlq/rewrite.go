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
	parents := parentsOf(root(docs[0]), nil, map[*yaml.Node]*yaml.Node{})
	lines := splitLines(src)
	out := src
	for _, m := range matches {
		if m.Node.Kind != yaml.ScalarNode {
			continue
		}
		value, ok := fn(m.Node.Value)
		if !ok {
			continue
		}
		replacement := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
		if m.Node.Style == yaml.DoubleQuotedStyle {
			replacement.Style = yaml.DoubleQuotedStyle
		}
		parent := parents[m.Node]
		if parent == nil {
			parent = root(docs[0])
		}
		if out, ok = replaceScalar(lines, parent, m.Node, replacement); !ok {
			return nil, invalid(path, "a scalar at line "+strconv.Itoa(m.Node.Line)+
				" cannot be rewritten in place")
		}
	}
	return out, nil
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
