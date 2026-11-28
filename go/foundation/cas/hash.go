package cas

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/gowebpki/jcs"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Canonical returns the RFC 8785 canonical form of raw JSON. Invalid JSON is
// CodeInvalidInput.
func Canonical(raw []byte) ([]byte, error) {
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "cas: canonicalize JSON")
	}

	return out, nil
}

// CanonicalOf marshals v to JSON and returns its canonical form. A value that does
// not marshal is CodeInvalidInput.
func CanonicalOf(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "cas: marshal value")
	}

	return Canonical(raw)
}

// Sum returns the content address of canonical bytes: SHA-256 over the domain, a NUL
// byte and canonical. The domain is not checked here; HashOf validates it.
func Sum(domain types.Domain, canonical []byte) types.Hash {
	digest := sha256.New()

	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(canonical)

	var out types.Hash

	copy(out[:], digest.Sum(nil))

	return out
}

// HashOf returns the content address of v under domain: Sum over CanonicalOf(value). An
// invalid domain or an unmarshalable value is CodeInvalidInput.
func HashOf(domain types.Domain, value any) (types.Hash, error) {
	err := domain.Validate()
	if err != nil {
		return types.Hash{}, err
	}

	canonical, err := CanonicalOf(value)
	if err != nil {
		return types.Hash{}, err
	}

	return Sum(domain, canonical), nil
}
