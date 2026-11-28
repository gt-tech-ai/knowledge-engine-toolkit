package types

import (
	"bytes"
	"encoding/hex"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// Hash is the SHA-256 content address of a stored value: the digest of a domain, a NUL
// and the value's canonical bytes (see foundation/cas).
type Hash [32]byte

// IsZero reports whether h is the zero hash, which stands for an absent value.
func (h Hash) IsZero() bool { return h == Hash{} }

// String renders h as lowercase hex.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// ParseHash reads a hash from its lowercase or uppercase hex form; anything else is
// CodeInvalidInput.
func ParseHash(text string) (Hash, error) {
	var hash Hash

	data, err := hex.DecodeString(text)
	if err != nil || len(data) != len(hash) {
		return Hash{}, apperr.New(apperr.CodeInvalidInput, "cas: invalid hash "+text)
	}

	copy(hash[:], data)

	return hash, nil
}

// MarshalText encodes h as lowercase hex, so a Hash in a struct is a JSON string.
func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText decodes the hex form ParseHash reads.
func (h *Hash) UnmarshalText(text []byte) error {
	parsed, err := ParseHash(string(text))
	if err != nil {
		return err
	}

	*h = parsed

	return nil
}

// Domain separates the hashes of different formats: it prefixes every hashed byte
// string, so bytes valid in two formats never share an address. Its version changes
// only with the format's encoding.
type Domain string

// Validate refuses a domain that cannot separate: empty, or holding the NUL byte that
// ends the prefix.
func (d Domain) Validate() error {
	if d == "" {
		return apperr.New(apperr.CodeInvalidInput, "cas: domain is empty")
	}

	if bytes.IndexByte([]byte(d), 0) >= 0 {
		return apperr.New(apperr.CodeInvalidInput, "cas: domain contains NUL")
	}

	return nil
}

// NodeKey addresses one node: the hash of its content within a scope (a tenant, or any
// owner string the caller picks). The same content under two scopes is two nodes.
type NodeKey struct {
	// Scope is the owner of the node, e.g. an organization id.
	Scope string
	// Hash is the content address.
	Hash Hash
}

// Node is one immutable stored value.
type Node struct {
	// Body is the node's bytes; for JSON content, the canonical form.
	Body []byte
	// Key addresses the node.
	Key NodeKey
}

// PutResult reports what a NodeStore.Put did.
type PutResult struct {
	// Stored is true when the call inserted the node, false when it already existed.
	Stored bool
	// Conflict is true when a node already existed under the key with a different
	// body: the stored body is kept, and the caller has found a hash collision or a
	// non-deterministic producer.
	Conflict bool
}
