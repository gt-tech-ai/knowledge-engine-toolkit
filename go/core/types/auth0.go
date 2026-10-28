package types

// SigningKey is an identity provider's token-signing key as a JWT verifier needs it.
type SigningKey struct {
	// PublicKeyPEM is the key's public half as a PEM "PUBLIC KEY" block.
	PublicKeyPEM string

	// KeyID is the key's JWKS kid, the value a token's header names it by.
	KeyID string
}
