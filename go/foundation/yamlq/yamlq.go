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

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	for rest := path[1:]; rest != ""; {
		st, tail, err := parseStep(path, rest)
		if err != nil {
			return nil, err
		}

		steps, rest = append(steps, st), tail
	}

	return steps, nil
}

// parseStep reads the step at the start of rest and returns what follows it.
func parseStep(path, rest string) (step, string, error) {
	switch {
	case strings.HasPrefix(rest, ".."):
		return parseKeyStep(path, rest[2:], stepDeep, "`..` needs a key")
	case strings.HasPrefix(rest, ".*"):
		return step{
			kind:  stepAllValues,
			key:   "",
			index: 0,
		}, rest[allValuesPrefixLen:], nil
	case strings.HasPrefix(rest, "."):
		return parseKeyStep(path, rest[1:], stepKey, "`.` needs a key")
	case strings.HasPrefix(rest, "["):
		return parseBracketStep(path, rest)
	default:
		return step{}, "", invalid(path, "unexpected "+strconv.Quote(rest))
	}
}

// parseKeyStep reads a key step of kind; an empty key fails with why.
func parseKeyStep(path, rest string, kind stepKind, why string) (step, string, error) {
	key, tail := splitKey(rest)
	if key == "" {
		return step{}, "", invalid(path, why)
	}

	return step{kind: kind, key: key, index: 0}, tail, nil
}

// parseBracketStep reads a `[*]` or `[n]` step.
func parseBracketStep(path, rest string) (step, string, error) {
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return step{}, "", invalid(path, "unclosed [")
	}

	inner, tail := rest[1:end], rest[end+1:]
	if inner == "*" {
		return step{kind: stepAllItems, key: "", index: 0}, tail, nil
	}

	n, err := strconv.Atoi(inner)
	if err != nil || n < 0 {
		return step{}, "", invalid(path, "an index is * or a non-negative integer")
	}

	return step{kind: stepIndex, index: n, key: ""}, tail, nil
}

// splitKey returns the key at the start of s and the rest of s.
func splitKey(text string) (key, rest string) {
	end := strings.IndexAny(text, ".[")
	if end < 0 {
		return text, ""
	}

	return text[:end], text[end:]
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
		if n = deref(n); n != nil {
			out = append(out, applyStep(n, st)...)
		}
	}

	return out
}

// applyStep returns what one step selects in a dereferenced node.
func applyStep(node *yaml.Node, currentStep step) []*yaml.Node {
	switch currentStep.kind {
	case stepKey:
		return oneNode(valueOf(node, currentStep.key))
	case stepDeep:
		return deep(node, currentStep.key)
	case stepAllValues:
		return mappingValues(node)
	case stepIndex:
		return oneNode(sequenceItem(node, currentStep.index))
	case stepAllItems:
		if node.Kind == yaml.SequenceNode {
			return node.Content
		}
	default:
		// No other value changes the outcome.
	}

	return nil
}

// oneNode wraps a selected node, turning a missing one into an empty selection.
func oneNode(n *yaml.Node) []*yaml.Node {
	if n == nil {
		return nil
	}

	return []*yaml.Node{n}
}

// sequenceItem returns item index of a sequence, or nil.
func sequenceItem(n *yaml.Node, index int) *yaml.Node {
	if n.Kind != yaml.SequenceNode || index >= len(n.Content) {
		return nil
	}

	return n.Content[index]
}

// mappingValues returns every value of a mapping, and nothing for any other node.
func mappingValues(node *yaml.Node) []*yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}

	var out []*yaml.Node

	for i := 1; i < len(node.Content); i += mappingStride {
		out = append(out, node.Content[i])
	}

	return out
}

// valueOf returns mapping n's value for key, or nil.
func valueOf(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(node.Content); i += mappingStride {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}

	return nil
}

// deep returns every value of key at or below n, in pre-order.
func deep(node *yaml.Node, key string) []*yaml.Node {
	node = deref(node)
	if node == nil {
		return nil
	}

	var out []*yaml.Node

	switch node.Kind {
	case yaml.MappingNode:
		out = deepMapping(node, key)
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range node.Content {
			out = append(out, deep(c, key)...)
		}
	case yaml.ScalarNode, yaml.AliasNode:
		// A scalar has no keys; deref already resolved an alias.
		return nil
	default:
		// No other value changes the outcome.
	}

	return out
}

// deepMapping collects key's values in a mapping and in everything below it.
func deepMapping(node *yaml.Node, key string) []*yaml.Node {
	var out []*yaml.Node

	for i := 0; i+1 < len(node.Content); i += mappingStride {
		if node.Content[i].Value == key {
			out = append(out, node.Content[i+1])
		}

		out = append(out, deep(node.Content[i+1], key)...)
	}

	return out
}

// matchOf describes one node.
func matchOf(n *yaml.Node) Match {
	match := Match{Node: n, Line: n.Line, Value: "", Kind: ""}
	switch n.Kind {
	case yaml.ScalarNode:
		match.Kind, match.Value = "scalar", n.Value
	case yaml.MappingNode:
		match.Kind = "mapping"
	case yaml.SequenceNode:
		match.Kind = "sequence"
	case yaml.AliasNode:
		match.Kind = "alias"
	case yaml.DocumentNode:
		match.Kind = "document"
	default:
		// No other value changes the outcome.
	}

	return match
}
