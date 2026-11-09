package chartspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/gowebpki/jcs"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// hashDomain prefixes every hashed node, so a node hash can never collide with a hash
// of some other format's bytes. The version changes only with the node encoding.
const hashDomain = "chartspec/v1\x00"

// Hash is the SHA-256 content address of a Node.
type Hash [32]byte

// IsZero reports whether h is the zero hash, which stands for an absent node.
func (h Hash) IsZero() bool { return h == Hash{} }

// String renders h as lowercase hex.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// ParseHash reads a hash from its lowercase or uppercase hex form; anything else is
// CodeInvalidInput.
func ParseHash(text string) (Hash, error) {
	var hash Hash

	data, err := hex.DecodeString(text)

	if err != nil || len(data) != len(hash) {
		return Hash{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: invalid node hash "+text,
		)
	}

	copy(hash[:], data)

	return hash, nil
}

// Node is one struct of a ChartSpec tree, addressed by the hash of its canonical
// encoding. Scalars sit in Fields; a singular child struct is referenced from Children
// and a repeated one from Lists, in order, so a change to one subtree changes only the
// hashes on its path to the root.
type Node struct {
	// Fields are the node's scalar members: strings, bools, numbers (json.Number or
	// float64) and, for an in/not_in operand, a []any of those. A nil value, typed nils
	// such as a nil slice included, is absent; Encode leaves out a struct member at its
	// zero value.
	Fields map[string]any

	// Children references each singular child node by hash.
	Children map[string]Hash

	// Lists references each repeated child, in order, by hash.
	Lists map[string][]Hash

	// Kind names the struct the node encodes (for example chart_spec or filter_clause).
	Kind string
}

// absent reports whether a field value is nil or a typed nil (a nil slice, map, pointer
// or interface), all of which a store round trip reads back as a missing field.
func absent(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// nodeWire is the JSON shape of a Node, with hashes as hex.
type nodeWire struct {
	// Fields are the scalar members.
	Fields map[string]any `json:"fields,omitempty"`

	// Children maps each singular child to its hex hash.
	Children map[string]string `json:"children,omitempty"`

	// Lists maps each repeated child to its hex hashes in order.
	Lists map[string][]string `json:"lists,omitempty"`

	// Kind names the struct.
	Kind string `json:"kind"`
}

// MarshalJSON writes the node as {kind, fields, children, lists} with hashes as hex,
// omitting nil fields (typed nils included), zero child hashes and empty lists (and a
// section left empty).
// Canonical is the hashed form of the same object.
func (n Node) MarshalJSON() ([]byte, error) {
	currentNodeWire := nodeWire{
		Kind:     n.Kind,
		Fields:   wireFields(n.Fields),
		Children: wireChildren(n.Children),
		Lists:    wireLists(n.Lists),
	}

	out, err := json.Marshal(currentNodeWire)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode node")
	}

	return out, nil
}

// wireFields copies the present fields; nil when none is.
func wireFields(fields map[string]any) map[string]any {
	var out map[string]any

	for text, value := range fields {
		if absent(value) {
			continue
		}

		if out == nil {
			out = map[string]any{}
		}

		out[text] = value
	}

	return out
}

// wireChildren renders the non-zero child hashes; nil when none is.
func wireChildren(children map[string]Hash) map[string]string {
	var out map[string]string

	for text, hash := range children {
		if hash.IsZero() {
			continue
		}

		if out == nil {
			out = map[string]string{}
		}

		out[text] = hash.String()
	}

	return out
}

// wireLists renders the non-empty lists; nil when none is.
func wireLists(lists map[string][]Hash) map[string][]string {
	var out map[string][]string

	for text, hashs := range lists {
		if len(hashs) == 0 {
			continue
		}

		if out == nil {
			out = map[string][]string{}
		}

		out[text] = hashStrings(hashs)
	}

	return out
}

// hashStrings renders hashes as hex.
func hashStrings(hs []Hash) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.String())
	}

	return out
}

// UnmarshalJSON reads a node from its JSON shape, keeping numbers as json.Number. A
// missing kind or a malformed hash is CodeInvalidInput.
func (n *Node) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var currentNodeWire nodeWire

	err := dec.Decode(&currentNodeWire)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: invalid node JSON")
	}

	if currentNodeWire.Kind == "" {
		return apperr.New(apperr.CodeInvalidInput, "chartspec: a node needs a kind")
	}

	children, err := parseChildren(currentNodeWire.Children)
	if err != nil {
		return err
	}

	lists, err := parseLists(currentNodeWire.Lists)
	if err != nil {
		return err
	}

	*n = Node{
		Kind:     currentNodeWire.Kind,
		Fields:   currentNodeWire.Fields,
		Children: children,
		Lists:    lists,
	}

	return nil
}

// parseChildren parses the child hashes; nil when there are none.
func parseChildren(wire map[string]string) (map[string]Hash, error) {
	var out map[string]Hash

	for text, s := range wire {
		hash, err := ParseHash(s)
		if err != nil {
			return nil, err
		}

		if out == nil {
			out = map[string]Hash{}
		}

		out[text] = hash
	}

	return out, nil
}

// parseLists parses the hash lists; nil when there are none, and an empty list has
// no key.
func parseLists(wire map[string][]string) (map[string][]Hash, error) {
	var out map[string][]Hash

	for k, ss := range wire {
		if out == nil {
			out = map[string][]Hash{}
		}

		err := appendParsedHashes(out, k, ss)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// appendParsedHashes parses ss onto list k of out.
func appendParsedHashes(out map[string][]Hash, text string, ss []string) error {
	for _, s := range ss {
		h, err := ParseHash(s)
		if err != nil {
			return err
		}

		out[text] = append(out[text], h)
	}

	return nil
}

// Canonical returns the node's RFC 8785 (JCS) encoding: its JSON shape with keys
// sorted, numbers in their shortest ECMAScript form and no insignificant whitespace.
// Equal nodes always encode to the same bytes.
func (n Node) Canonical() ([]byte, error) {
	raw, err := n.MarshalJSON()
	if err != nil {
		return nil, err
	}

	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"chartspec: canonicalize node",
		)
	}

	return out, nil
}

// Hash returns the node's content address: SHA-256 over the domain prefix
// "chartspec/v1\x00" followed by Canonical.
func (n Node) Hash() (Hash, error) {
	canonical, err := n.Canonical()
	if err != nil {
		return Hash{}, err
	}

	hash := sha256.New()

	_, _ = hash.Write([]byte(hashDomain))
	_, _ = hash.Write(canonical)

	var out Hash

	copy(out[:], hash.Sum(nil))

	return out, nil
}
