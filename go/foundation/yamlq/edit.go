package yamlq

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// mapTag is the YAML mapping tag.
const mapTag = "!!map"

// Upsert sets the value at path in the first document of src to value, creating
// missing mapping keys, and returns the edited file.
func Upsert(src []byte, path string, value any) ([]byte, error) {
	return UpsertAt(src, 0, path, value)
}

// UpsertAt sets the value at path in document index of src. The path may use only
// `.key` and `[n]` steps. The edit is spliced into the source text, so every byte the
// edit does not touch (comments, blank lines, key order, the other documents) is kept:
// a scalar replacing a single-line block scalar rewrites only that token; missing keys
// are appended as a block after the last line of their block mapping; a null on the
// path (`key:`, `key: ~`, `key: null`) becomes a mapping holding the missing keys; a
// source with no document (empty, or only comments) is an empty mapping, the keys
// appended after its lines. Any other change (a collection replacing or replacing a
// node, a scalar in a flow collection or one that wraps onto the next line, an append
// to a flow mapping) re-encodes the file with two-space indentation instead. A missing
// sequence item is CodeInvalidInput.
func UpsertAt(src []byte, index int, path string, value any) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "encode the value")
	}
	if docs, err := DecodeAll(src); err == nil && len(docs) == 0 && index == 0 {
		return upsertEmpty(src, path, &node)
	}
	steps, docs, err := prepare(src, index, path)
	if err != nil {
		return nil, err
	}
	e := upsert{lines: splitLines(src), docs: docs, index: index, path: path, node: &node}
	return e.run(steps)
}

// upsert is one UpsertAt edit: the source lines, its decoded documents, and the value
// node to place at path in document index.
type upsert struct {
	// node is the value being set.
	node *yaml.Node

	// path is the edit's path, for the holds check and error messages.
	path string

	// lines are the source, one entry per line.
	lines []string

	// docs are the decoded documents; a re-encode writes them back.
	docs []*yaml.Node

	// index is the document the edit targets.
	index int
}

// run walks steps from the document root: a null on the way becomes a mapping, the
// leaf is replaced (upsertLeaf), and the first missing key starts the keys to create
// (upsertMissing).
func (e upsert) run(steps []step) ([]byte, error) {
	cur := root(e.docs[e.index])
	for i, st := range steps {
		cur = deref(cur)
		next, err := child(cur, st)
		if err != nil {
			return nil, err
		}
		switch {
		case next == nil:
			return e.missing(cur, steps[i:])
		case i == len(steps)-1:
			return e.leaf(cur, next)
		case isNull(next):
			out, ok := spliceUnderNull(e.lines, cur, st, steps[i+1:], e.node)
			if ok && holds(out, e.index, e.path, e.node) {
				return out, nil
			}
			*next = yaml.Node{Kind: yaml.MappingNode, Tag: mapTag}
		}
		cur = next
	}
	return nil, invalid(e.path, "an edit needs at least one step")
}

// leaf replaces the existing value old under parent: spliced over its token when it can
// be, else re-encoded with old's comments kept.
func (e upsert) leaf(parent, old *yaml.Node) ([]byte, error) {
	if out, ok := replaceScalar(e.lines, parent, old, e.node); ok &&
		holds(out, e.index, e.path, e.node) {
		return out, nil
	}
	keepComments(e.node, old)
	*old = *e.node
	return encodeAll(e.docs)
}

// missing creates steps' keys under parent, ending in the value: appended as a block
// when it can be, else re-encoded.
func (e upsert) missing(parent *yaml.Node, steps []step) ([]byte, error) {
	if out, ok := appendKeys(e.lines, parent, steps, e.node); ok {
		return out, nil
	}
	if err := build(parent, steps, e.node); err != nil {
		return nil, err
	}
	return encodeAll(e.docs)
}

// Append adds value as the last item of the sequence at path in the first document of
// src and returns the edited file.
func Append(src []byte, path string, value any) ([]byte, error) {
	return AppendAt(src, 0, path, value)
}

// AppendAt adds value as the last item of the sequence at path in document index of src.
// The path may use only `.key` and `[n]` steps and must select a sequence. Into a
// non-empty block sequence the item is spliced after the last item's last line, at the
// items' indentation, so every other byte is kept; a flow or empty sequence re-encodes
// the file with two-space indentation instead.
func AppendAt(src []byte, index int, path string, value any) ([]byte, error) {
	steps, docs, err := prepare(src, index, path)
	if err != nil {
		return nil, err
	}
	seq := root(docs[index])
	for _, st := range steps {
		if seq, err = child(deref(seq), st); err != nil {
			return nil, err
		}
		if seq == nil {
			return nil, invalid(path, "selects nothing")
		}
	}
	seq = deref(seq)
	if seq.Kind != yaml.SequenceNode {
		return nil, invalid(path, "does not select a sequence")
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "encode the value")
	}
	if out, ok := appendItem(splitLines(src), seq, &node); ok {
		return out, nil
	}
	seq.Content = append(seq.Content, &node)
	return encodeAll(docs)
}

// appendItem splices item after the last line of block sequence seq, its dash indented
// like the items', returning false when seq is a flow or empty sequence.
func appendItem(lines []string, seq, item *yaml.Node) ([]byte, bool) {
	if seq.Style&yaml.FlowStyle != 0 || len(seq.Content) == 0 {
		return nil, false
	}
	last := seq.Content[len(seq.Content)-1]
	dash := last.Column - 3
	if dash < 0 || last.Line < 1 || last.Line > len(lines) ||
		len(lines[last.Line-1]) < dash+2 ||
		!strings.HasPrefix(lines[last.Line-1][dash:], "- ") {
		return nil, false
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	block := &yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Content: []*yaml.Node{item},
	}
	if enc.Encode(block) != nil || enc.Close() != nil {
		return nil, false
	}
	indent := strings.Repeat(" ", dash)
	insert := splitLines(buf.Bytes())
	for i, l := range insert {
		if l != "" {
			insert[i] = indent + l
		}
	}
	at := maxLine(last)
	out := append(append(lines[:at:at], insert...), lines[at:]...)
	return joinLines(out), true
}

// Delete removes the node at path from the first document of src.
func Delete(src []byte, path string) ([]byte, error) {
	return DeleteAt(src, 0, path)
}

// DeleteAt removes the mapping entry or sequence item at path from document index of
// src by deleting its lines (with the entry's head comment); a path that selects
// nothing leaves the file unchanged. An entry that shares its lines with others (a flow
// collection) re-encodes the file instead.
func DeleteAt(src []byte, index int, path string) ([]byte, error) {
	steps, docs, err := prepare(src, index, path)
	if err != nil {
		return nil, err
	}
	parent := root(docs[index])
	for _, st := range steps[:len(steps)-1] {
		if parent, err = child(deref(parent), st); err != nil || parent == nil {
			return src, err
		}
	}
	parent = deref(parent)
	last := steps[len(steps)-1]
	first, entry, pos := entryOf(parent, last)
	if entry == nil {
		return src, nil
	}
	if parent.Style&yaml.FlowStyle == 0 {
		lines := splitLines(src)
		from := first.Line - strings.Count(first.HeadComment, "\n") - 1
		if first.HeadComment != "" {
			from--
		}
		to := maxLine(entry)
		return joinLines(append(lines[:from:from], lines[to:]...)), nil
	}
	parent.Content = append(parent.Content[:pos], parent.Content[pos+width(parent):]...)
	return encodeAll(docs)
}

// prepare parses path and src and checks index.
func prepare(src []byte, index int, path string) ([]step, []*yaml.Node, error) {
	steps, err := parse(path)
	if err != nil {
		return nil, nil, err
	}
	if len(steps) == 0 {
		return nil, nil, invalid(path, "an edit needs at least one step")
	}
	for _, st := range steps {
		if st.kind != stepKey && st.kind != stepIndex {
			return nil, nil, invalid(path, "an edit path uses only .key and [n] steps")
		}
	}
	docs, err := DecodeAll(src)
	if err != nil {
		return nil, nil, err
	}
	if index < 0 || index >= len(docs) {
		return nil, nil, apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("document %d of %d", index, len(docs)))
	}
	return steps, docs, nil
}

// child returns n's node for st: nil for a missing mapping key, CodeInvalidInput for a
// missing sequence item or a step that does not fit n.
func child(n *yaml.Node, st step) (*yaml.Node, error) {
	if st.kind == stepIndex {
		if n.Kind != yaml.SequenceNode || st.index >= len(n.Content) {
			return nil, apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("no sequence item %d", st.index))
		}
		return n.Content[st.index], nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, apperr.New(apperr.CodeInvalidInput, "not a mapping at "+st.key)
	}
	return valueOf(n, st.key), nil
}

// entryOf returns the node that starts last's entry in parent (the key, or the item),
// the entry's value and its index in parent.Content.
func entryOf(parent *yaml.Node, last step) (first, value *yaml.Node, pos int) {
	switch {
	case last.kind == stepIndex && parent.Kind == yaml.SequenceNode:
		if last.index < len(parent.Content) {
			return parent.Content[last.index], parent.Content[last.index], last.index
		}
	case last.kind == stepKey && parent.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i].Value == last.key {
				return parent.Content[i], parent.Content[i+1], i
			}
		}
	}
	return nil, nil, 0
}

// width is how many Content slots one entry of n takes.
func width(n *yaml.Node) int {
	if n.Kind == yaml.MappingNode {
		return 2
	}
	return 1
}

// replaceScalar splices replacement's text over old's when both are scalars, parent is a
// block collection and only a comment follows old's token on its line, returning false
// when the splice does not apply. A scalar that wraps onto the next line is caught by
// the caller's holds check.
func replaceScalar(lines []string, parent, old, replacement *yaml.Node) ([]byte, bool) {
	if old.Kind != yaml.ScalarNode || replacement.Kind != yaml.ScalarNode ||
		parent.Style&yaml.FlowStyle != 0 ||
		old.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 ||
		old.Line < 1 || old.Line > len(lines) {
		return nil, false
	}
	line := lines[old.Line-1]
	start := old.Column - 1
	if start < 0 || start > len(line) {
		return nil, false
	}
	end := start + tokenLen(line[start:])
	if rest := strings.TrimSpace(
		line[end:],
	); rest != "" &&
		!strings.HasPrefix(rest, "#") {
		return nil, false
	}
	text, err := yaml.Marshal(replacement)
	if err != nil {
		return nil, false
	}
	token := strings.TrimSuffix(string(text), "\n")
	if strings.Contains(token, "\n") {
		return nil, false
	}
	lines[old.Line-1] = line[:start] + token + line[end:]
	return joinLines(lines), true
}

// holds reports whether out parses and its value at path in document index is exactly
// replacement's: a scalar that wrapped onto the next line leaves its continuation behind,
// which a splice of its first line alone cannot remove.
func holds(out []byte, index int, path string, replacement *yaml.Node) bool {
	docs, err := DecodeAll(out)
	if err != nil || index >= len(docs) {
		return false
	}
	got, err := Path(docs[index], path)
	return err == nil && len(got) == 1 && got[0].Node.Kind == yaml.ScalarNode &&
		got[0].Value == replacement.Value
}

// tokenLen is the length of the scalar token that starts s: a quoted string through its
// closing quote, else everything up to a ` #` comment, trailing spaces trimmed.
func tokenLen(s string) int {
	if s != "" && (s[0] == '"' || s[0] == '\'') {
		q := s[0]
		for i := 1; i < len(s); i++ {
			switch {
			case q == '"' && s[i] == '\\':
				i++
			case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
				i++
			case s[i] == q:
				return i + 1
			}
		}
		return len(s)
	}
	end := len(s)
	if i := strings.Index(s, " #"); i >= 0 {
		end = i
	}
	return len(strings.TrimRight(s[:end], " \t"))
}

// appendKeys inserts the missing keys of steps (all mapping keys) with value as a block
// after the last line of block mapping parent, indented like its keys.
func appendKeys(lines []string, parent *yaml.Node, steps []step, value *yaml.Node) (
	[]byte, bool,
) {
	if parent.Kind != yaml.MappingNode || parent.Style&yaml.FlowStyle != 0 ||
		len(parent.Content) == 0 {
		return nil, false
	}
	for _, st := range steps {
		if st.kind != stepKey {
			return nil, false
		}
	}
	insert, ok := encodeBlock(
		steps,
		value,
		strings.Repeat(" ", parent.Content[0].Column-1),
	)
	if !ok {
		return nil, false
	}
	at := maxLine(parent)
	out := append(append(lines[:at:at], insert...), lines[at:]...)
	return joinLines(out), true
}

// upsertEmpty appends path's keys, ending in value, as a block mapping after the lines
// of src, a source holding no document. A path with a sequence step is CodeInvalidInput.
func upsertEmpty(src []byte, path string, value *yaml.Node) ([]byte, error) {
	steps, err := parse(path)
	if err != nil {
		return nil, err
	}
	insert, ok := encodeBlock(steps, value, "")
	if !ok || len(steps) == 0 {
		return nil, invalid(path, "an empty document takes only .key steps")
	}
	return joinLines(append(splitLines(src), insert...)), nil
}

// isNull reports whether n is a YAML null scalar.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// spliceUnderNull turns the block-mapping entry `key: <null>` (st's entry in parent) into
// a mapping header and splices rest's keys, ending in value, beneath it at two more
// spaces, keeping the entry's inline comment; it returns false when the entry is not a
// plain key and a null token on one line.
func spliceUnderNull(
	lines []string, parent *yaml.Node, st step, rest []step, value *yaml.Node,
) ([]byte, bool) {
	key, _, _ := entryOf(parent, st)
	if key == nil || parent.Style&yaml.FlowStyle != 0 || key.Style != 0 ||
		key.Line < 1 || key.Line > len(lines) {
		return nil, false
	}
	header, ok := nullHeader(lines[key.Line-1], key)
	if !ok {
		return nil, false
	}
	insert, ok := encodeBlock(rest, value, strings.Repeat(" ", key.Column+1))
	if !ok {
		return nil, false
	}
	out := append(append(append([]string{}, lines[:key.Line-1]...), header), insert...)
	return joinLines(append(out, lines[key.Line:]...)), true
}

// nullHeader returns line, the plain key's `key: <null> [# comment]` line, as a mapping
// header `key: [# comment]`; false when anything but a null token and a comment follows
// the colon.
func nullHeader(line string, key *yaml.Node) (string, bool) {
	end := key.Column - 1 + len(key.Value)
	if end >= len(line) || line[end] != ':' {
		return "", false
	}
	tail := strings.TrimSpace(line[end+1:])
	for _, token := range []string{"~", "null", "Null", "NULL"} {
		if t, ok := strings.CutPrefix(tail, token); ok && (t == "" || t[0] == ' ') {
			tail = strings.TrimSpace(t)
			break
		}
	}
	if tail == "" {
		return line[:end+1], true
	}
	if !strings.HasPrefix(tail, "#") {
		return "", false
	}
	return line[:end+1] + " " + tail, true
}

// encodeBlock renders steps' keys, ending in value, as a two-space block mapping whose
// lines each start with indent; false when the steps cannot form one.
func encodeBlock(steps []step, value *yaml.Node, indent string) ([]string, bool) {
	block := &yaml.Node{Kind: yaml.MappingNode, Tag: mapTag}
	if build(block, steps, value) != nil {
		return nil, false
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if enc.Encode(block) != nil || enc.Close() != nil {
		return nil, false
	}
	lines := splitLines(buf.Bytes())
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return lines, true
}

// build adds steps' missing keys under n, ending in value.
func build(n *yaml.Node, steps []step, value *yaml.Node) error {
	for i, st := range steps {
		if st.kind != stepKey || n.Kind != yaml.MappingNode {
			return apperr.New(apperr.CodeInvalidInput, "cannot create a sequence item")
		}
		if i == len(steps)-1 {
			n.Content = append(n.Content, keyNode(st.key), value)
			return nil
		}
		next := &yaml.Node{Kind: yaml.MappingNode, Tag: mapTag}
		n.Content = append(n.Content, keyNode(st.key), next)
		n = next
	}
	return nil
}

// maxLine is the greatest source line of n and its descendants, extended over a block
// scalar's continuation lines.
func maxLine(n *yaml.Node) int {
	m := n.Line
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		m += strings.Count(strings.TrimSuffix(n.Value, "\n"), "\n") + 1
	}
	for _, c := range n.Content {
		m = max(m, maxLine(c))
	}
	return m
}

// keepComments copies old's comments onto replacement.
func keepComments(replacement, old *yaml.Node) {
	replacement.HeadComment = old.HeadComment
	replacement.LineComment = old.LineComment
	replacement.FootComment = old.FootComment
}

// keyNode is a plain string mapping key.
func keyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

// splitLines splits src into lines without terminators; a final newline adds no line.
func splitLines(src []byte) []string {
	s := strings.TrimSuffix(string(src), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// joinLines joins lines with newlines and a final newline.
func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// DecodeAll decodes every document of src, in order. A malformed document is
// CodeInvalidInput.
func DecodeAll(src []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err == io.EOF { //nolint:errorlint // the decoder returns io.EOF unwrapped
			return docs, nil
		}
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "decode yaml")
		}
		docs = append(docs, &doc)
	}
}

// encodeAll re-encodes docs with two-space indentation, separated by `---`.
func encodeAll(docs []*yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "encode yaml")
		}
	}
	if err := enc.Close(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "encode yaml")
	}
	return buf.Bytes(), nil
}
