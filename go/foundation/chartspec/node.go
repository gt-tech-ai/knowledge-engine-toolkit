package chartspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gowebpki/jcs"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
func ParseHash(s string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(h) {
		return Hash{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: invalid node hash "+s,
		)
	}
	copy(h[:], b)
	return h, nil
}

// Node is one struct of a ChartSpec tree, addressed by the hash of its canonical
// encoding. Scalars sit in Fields; a singular child struct is referenced from Children
// and a repeated one from Lists, in order, so a change to one subtree changes only the
// hashes on its path to the root.
type Node struct {
	// Fields are the node's scalar members: strings, bools, numbers (json.Number or
	// float64) and, for an in/not_in operand, a []any of those. A nil value is absent;
	// Encode leaves out a struct member at its zero value.
	Fields map[string]any

	// Children references each singular child node by hash.
	Children map[string]Hash

	// Lists references each repeated child, in order, by hash.
	Lists map[string][]Hash

	// Kind names the struct the node encodes (for example chart_spec or filter_clause).
	Kind string
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
// omitting nil fields, zero child hashes and empty lists (and a section left empty).
// Canonical is the hashed form of the same object.
func (n Node) MarshalJSON() ([]byte, error) {
	w := nodeWire{Kind: n.Kind}
	for k, v := range n.Fields {
		if v == nil {
			continue
		}
		if w.Fields == nil {
			w.Fields = map[string]any{}
		}
		w.Fields[k] = v
	}
	for k, h := range n.Children {
		if h.IsZero() {
			continue
		}
		if w.Children == nil {
			w.Children = map[string]string{}
		}
		w.Children[k] = h.String()
	}
	for k, hs := range n.Lists {
		if len(hs) == 0 {
			continue
		}
		if w.Lists == nil {
			w.Lists = map[string][]string{}
		}
		for _, h := range hs {
			w.Lists[k] = append(w.Lists[k], h.String())
		}
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode node")
	}
	return out, nil
}

// UnmarshalJSON reads a node from its JSON shape, keeping numbers as json.Number. A
// missing kind or a malformed hash is CodeInvalidInput.
func (n *Node) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var w nodeWire
	if err := dec.Decode(&w); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: invalid node JSON")
	}
	if w.Kind == "" {
		return apperr.New(apperr.CodeInvalidInput, "chartspec: a node needs a kind")
	}
	node := Node{Kind: w.Kind, Fields: w.Fields}
	for k, s := range w.Children {
		h, err := ParseHash(s)
		if err != nil {
			return err
		}
		if node.Children == nil {
			node.Children = map[string]Hash{}
		}
		node.Children[k] = h
	}
	for k, ss := range w.Lists {
		if node.Lists == nil {
			node.Lists = map[string][]Hash{}
		}
		for _, s := range ss {
			h, err := ParseHash(s)
			if err != nil {
				return err
			}
			node.Lists[k] = append(node.Lists[k], h)
		}
	}
	*n = node
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
	h := sha256.New()
	h.Write([]byte(hashDomain))
	h.Write(canonical)
	var out Hash
	copy(out[:], h.Sum(nil))
	return out, nil
}
