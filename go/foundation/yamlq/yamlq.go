// Package yamlq queries and edits YAML documents by path. Path selects nodes with a
// small JSONPath subset; Upsert and Delete change one node and re-encode the document
// so comments, key order and the other documents of the file are kept.
//
// The path language: `$` is the document's root; `.key` selects a mapping value;
// `..key` selects the value of key at any depth (pre-order); `.*` selects every value
// of a mapping; `[n]` selects one sequence item and `[*]` every item.
package yamlq

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Match is one node a path selected.
type Match struct {
	// Node is the selected node.
	Node *yaml.Node

	// Value is the node's value for a scalar; empty otherwise.
	Value string

	// Kind is "scalar", "mapping", "sequence" or "alias".
	Kind string

	// Line is the node's 1-based line.
	Line int
}

// stepKind is one kind of path step.
type stepKind int

const (
	// stepKey selects one mapping value.
	stepKey stepKind = iota
	// stepDeep selects one key's value at any depth.
	stepDeep
	// stepAllValues selects every mapping value.
	stepAllValues
	// stepIndex selects one sequence item.
	stepIndex
	// stepAllItems selects every sequence item.
	stepAllItems
)

// step is one parsed path step.
type step struct {
	// key is the mapping key of a stepKey or stepDeep.
	key string
	// kind is the step's kind.
	kind stepKind
	// index is the item of a stepIndex.
	index int
}

// parse splits path into steps; a malformed path is CodeInvalidInput.
func parse(path string) ([]step, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, invalid(path, "a path starts with $")
	}
	var steps []step
	rest := path[1:]
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, ".."):
			key, tail := splitKey(rest[2:])
			if key == "" {
				return nil, invalid(path, "`..` needs a key")
			}
			steps, rest = append(steps, step{kind: stepDeep, key: key}), tail
		case strings.HasPrefix(rest, ".*"):
			steps, rest = append(steps, step{kind: stepAllValues}), rest[2:]
		case strings.HasPrefix(rest, "."):
			key, tail := splitKey(rest[1:])
			if key == "" {
				return nil, invalid(path, "`.` needs a key")
			}
			steps, rest = append(steps, step{kind: stepKey, key: key}), tail
		case strings.HasPrefix(rest, "["):
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, invalid(path, "unclosed [")
			}
			inner := rest[1:end]
			if inner == "*" {
				steps = append(steps, step{kind: stepAllItems})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 {
					return nil, invalid(path, "an index is * or a non-negative integer")
				}
				steps = append(steps, step{kind: stepIndex, index: n})
			}
			rest = rest[end+1:]
		default:
			return nil, invalid(path, "unexpected "+strconv.Quote(rest))
		}
	}
	return steps, nil
}

// splitKey returns the key at the start of s and the rest of s.
func splitKey(s string) (key, rest string) {
	end := strings.IndexAny(s, ".[")
	if end < 0 {
		return s, ""
	}
	return s[:end], s[end:]
}

// invalid is the CodeInvalidInput error for a malformed path.
func invalid(path, why string) error {
	return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf("yaml path %q: %s", path, why))
}

// Path returns the nodes path selects in node, a document or any node within one.
func Path(node *yaml.Node, path string) ([]Match, error) {
	steps, err := parse(path)
	if err != nil {
		return nil, err
	}
	cur := []*yaml.Node{root(node)}
	for _, st := range steps {
		cur = apply(cur, st)
	}
	out := make([]Match, 0, len(cur))
	for _, n := range cur {
		out = append(out, matchOf(n))
	}
	return out, nil
}

// root returns a document's content node, or node itself.
func root(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

// deref resolves an alias to its anchor.
func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// apply runs one step over every current node.
func apply(cur []*yaml.Node, st step) []*yaml.Node {
	var out []*yaml.Node
	for _, n := range cur {
		n = deref(n)
		if n == nil {
			continue
		}
		switch st.kind {
		case stepKey:
			if v := valueOf(n, st.key); v != nil {
				out = append(out, v)
			}
		case stepDeep:
			out = append(out, deep(n, st.key)...)
		case stepAllValues:
			if n.Kind == yaml.MappingNode {
				for i := 1; i < len(n.Content); i += 2 {
					out = append(out, n.Content[i])
				}
			}
		case stepIndex:
			if n.Kind == yaml.SequenceNode && st.index < len(n.Content) {
				out = append(out, n.Content[st.index])
			}
		case stepAllItems:
			if n.Kind == yaml.SequenceNode {
				out = append(out, n.Content...)
			}
		}
	}
	return out
}

// valueOf returns mapping n's value for key, or nil.
func valueOf(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// deep returns every value of key at or below n, in pre-order.
func deep(n *yaml.Node, key string) []*yaml.Node {
	n = deref(n)
	if n == nil {
		return nil
	}
	var out []*yaml.Node
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				out = append(out, n.Content[i+1])
			}
			out = append(out, deep(n.Content[i+1], key)...)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			out = append(out, deep(c, key)...)
		}
	case yaml.ScalarNode, yaml.AliasNode:
		// A scalar has no keys; deref already resolved an alias.
	}
	return out
}

// matchOf describes one node.
func matchOf(n *yaml.Node) Match {
	m := Match{Node: n, Line: n.Line}
	switch n.Kind {
	case yaml.ScalarNode:
		m.Kind, m.Value = "scalar", n.Value
	case yaml.MappingNode:
		m.Kind = "mapping"
	case yaml.SequenceNode:
		m.Kind = "sequence"
	case yaml.AliasNode:
		m.Kind = "alias"
	case yaml.DocumentNode:
		m.Kind = "document"
	}
	return m
}
