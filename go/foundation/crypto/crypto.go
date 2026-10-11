// Package crypto provides authenticated symmetric encryption for data at rest —
// e.g. connector credentials sealed before they are written to Postgres.
// The core interfaces.Encryptor contract is backed here by AES-256-GCM with a fresh
// random nonce per message; the key is injected from configuration, never hardcoded.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// KeySize is the required key length in bytes for AES-256.
const KeySize = 32

// aesGCM is the AES-256-GCM implementation of the core interfaces.Encryptor contract.
type aesGCM struct {
	// gcm is the authenticated cipher built from the key.
	gcm cipher.AEAD
}

// Compile-time assertion that *aesGCM satisfies interfaces.Encryptor.
var _ interfaces.Encryptor = (*aesGCM)(nil)

// New builds an AES-256-GCM interfaces.Encryptor over a 32-byte key. It fails loudly on
// a key of the wrong length rather than silently truncating or padding it.
func New(key []byte) (interfaces.Encryptor, error) {
	if len(key) != KeySize {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("crypto: key must be %d bytes, got %d", KeySize, len(key)),
		)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "crypto: new cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "crypto: new gcm")
	}
	return &aesGCM{gcm: gcm}, nil
}

// Encrypt seals plaintext with a fresh random nonce and returns nonce||ciphertext,
// so the nonce travels with the ciphertext for Decrypt.
func (a *aesGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, a.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "crypto: read nonce")
	}
	// Seal appends the sealed ciphertext to the nonce prefix passed as dst.
	return a.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt splits the nonce prefix from the sealed ciphertext and opens it, failing
// on a wrong key or a tampered payload (the GCM authentication tag).
func (a *aesGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	nonceSize := a.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"crypto: ciphertext shorter than nonce",
		)
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := a.gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"crypto: decrypt failed (wrong key or tampered ciphertext)",
		)
	}
	return plaintext, nil
}
