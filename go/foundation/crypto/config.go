package crypto

import (
	"encoding/base64"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Config carries the encryption key for NewFromConfig. The key is base64-encoded
// (32 bytes decoded) and sourced from secrets configuration — never a hardcoded literal.
type Config struct {
	// Key is the base64-encoded AES-256 key (32 bytes once decoded).
	Key string
}

// NewFromConfig builds an interfaces.Encryptor from cfg: it base64-decodes the key and
// fails loudly on a missing key, invalid base64, or a decoded length other than 32 bytes
// (New enforces the length), so bad key material is rejected at startup, not first use.
func NewFromConfig(cfg Config) (interfaces.Encryptor, error) {
	if cfg.Key == "" {
		return nil, apperr.New(apperr.CodeInvalidInput, "crypto: key is required")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.Key)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"crypto: key is not valid base64",
		)
	}
	return New(key)
}
