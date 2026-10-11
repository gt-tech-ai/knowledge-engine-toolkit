package interfaces

// Encryptor seals and opens byte payloads with authenticated encryption. Encrypt
// returns an opaque, nonce-prefixed ciphertext; Decrypt reverses it and fails on a
// wrong key or a tampered ciphertext (the authentication tag). It is the shared
// data-at-rest sealing contract — e.g. connector credentials sealed before they are
// written to Postgres. The concrete backend (AES-256-GCM, in
// foundation/crypto) is injected from configuration, never referenced directly.
type Encryptor interface {
	// Encrypt seals plaintext, returning a nonce-prefixed ciphertext.
	Encrypt(plaintext []byte) ([]byte, error)
	// Decrypt opens a ciphertext produced by Encrypt, returning the original plaintext.
	// It errors on a wrong key or a tampered ciphertext.
	Decrypt(ciphertext []byte) ([]byte, error)
}
