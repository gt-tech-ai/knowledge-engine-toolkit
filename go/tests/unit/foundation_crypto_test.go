package unit_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/crypto"
)

// testKey is a fixed 32-byte AES-256 key for the crypto unit tests.
func testKey() []byte { return bytes.Repeat([]byte{0x2b}, crypto.KeySize) }

// TestEncryptor_RoundTrip tests that AES-GCM Encrypt/Decrypt round-trips a payload and
// that the ciphertext both hides the plaintext and differs across calls (random nonce).
//
// Why this test is important:
//   - This is the data-at-rest guarantee connector credentials rely on: whatever is
//     sealed must open back to the exact bytes, the stored ciphertext must not leak the
//     plaintext, and a deterministic ciphertext (reused nonce) would be a real
//     cryptographic weakness.
//
// What it tests:
//   - Decrypt(Encrypt(p)) == p; the ciphertext != p and is longer (nonce + tag); two
//     encryptions of the same plaintext produce different ciphertexts.
func TestEncryptor_RoundTrip(t *testing.T) {
	t.Parallel()
	enc, err := crypto.New(testKey())
	require.NoError(t, err)

	plaintext := []byte("iam_role_arn=arn:aws:iam::123:role/connector")
	ct1, err := enc.Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ct1, "ciphertext must not equal plaintext")
	assert.Greater(t, len(ct1), len(plaintext), "ciphertext carries a nonce + auth tag")

	got, err := enc.Decrypt(ct1)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)

	ct2, err := enc.Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, ct1, ct2, "a random nonce makes each ciphertext unique")
}

// TestEncryptor_WrongKeyFailsToDecrypt tests that a ciphertext sealed under one key
// cannot be opened with a different key.
//
// Why this test is important:
//   - Key confidentiality is the whole point: an attacker (or a misconfigured key
//     rotation) with the ciphertext but not the key must not recover the credentials.
//
// What it tests:
//   - Decrypt with a different key returns an error and no plaintext.
func TestEncryptor_WrongKeyFailsToDecrypt(t *testing.T) {
	t.Parallel()
	encA, err := crypto.New(testKey())
	require.NoError(t, err)
	encB, err := crypto.New(bytes.Repeat([]byte{0x3c}, crypto.KeySize))
	require.NoError(t, err)

	ct, err := encA.Encrypt([]byte("secret"))
	require.NoError(t, err)

	_, err = encB.Decrypt(ct)
	require.Error(t, err, "a wrong key must not decrypt")
}

// TestEncryptor_TamperedCiphertextFails tests that flipping any byte of the ciphertext
// makes Decrypt fail (the GCM authentication tag).
//
// Why this test is important:
//   - Authenticated encryption is what protects stored credentials from silent tampering;
//     a decrypt that ignored the tag would return attacker-controlled bytes.
//
// What it tests:
//   - A single flipped byte in the ciphertext causes Decrypt to error.
func TestEncryptor_TamperedCiphertextFails(t *testing.T) {
	t.Parallel()
	enc, err := crypto.New(testKey())
	require.NoError(t, err)
	ct, err := enc.Encrypt([]byte("secret"))
	require.NoError(t, err)

	ct[len(ct)-1] ^= 0xff // flip the last byte (inside the auth tag)
	_, err = enc.Decrypt(ct)
	require.Error(t, err, "tampered ciphertext must fail authentication")
}

// TestNewFromConfig tests the config-driven constructor: it accepts a base64 32-byte key
// and fails loudly on a missing, malformed, or wrong-length key.
//
// Why this test is important:
//   - The key is injected from secrets config; a silent acceptance of an empty/short key
//     would produce a weak or all-zero cipher, so the constructor must reject bad key
//     material at startup, not at first use.
//
// What it tests:
//   - A valid base64 32-byte key builds a working Encryptor; an empty key, non-base64,
//     and a decoded length != 32 each return an error.
func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	validKey := base64.StdEncoding.EncodeToString(testKey())

	enc, err := crypto.NewFromConfig(crypto.Config{Key: validKey})
	require.NoError(t, err)
	require.NotNil(t, enc)

	for name, key := range map[string]string{
		"empty":        "",
		"not base64":   "not-base64-!!!",
		"wrong length": base64.StdEncoding.EncodeToString([]byte("too-short")),
		"zero-len key": base64.StdEncoding.EncodeToString([]byte{}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := crypto.NewFromConfig(crypto.Config{Key: key})
			require.Error(t, err)
		})
	}
}

// TestNew_RejectsWrongKeyLength tests that New fails loudly on a non-32-byte key rather
// than truncating/padding.
//
// Why this test is important:
//   - Silently accepting a wrong-length key is a classic footgun (weak key); AES-256
//     requires exactly 32 bytes and the constructor must enforce it.
//
// What it tests:
//   - A 16-byte and a 0-byte key each return an error.
func TestNew_RejectsWrongKeyLength(t *testing.T) {
	t.Parallel()
	_, err := crypto.New(bytes.Repeat([]byte{1}, 16))
	require.Error(t, err)
	_, err = crypto.New(nil)
	require.Error(t, err)
}

// FuzzEncryptRoundtrip fuzzes the encrypt→decrypt roundtrip over arbitrary plaintext.
//
// Why this test is important:
//   - Credentials are arbitrary bytes (empty, binary, large); the roundtrip invariant
//     must hold for every input, not just the hand-picked examples above.
//
// What it tests:
//   - For any plaintext, Decrypt(Encrypt(p)) == p.
func FuzzEncryptRoundtrip(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("access_key_id=AKIA...;secret=...."))
	f.Add(make([]byte, 1024))
	enc, err := crypto.New(testKey())
	require.NoError(f, err)

	f.Fuzz(func(t *testing.T, plaintext []byte) {
		ct, err := enc.Encrypt(plaintext)
		require.NoError(t, err)
		got, err := enc.Decrypt(ct)
		require.NoError(t, err)
		// bytes.Equal (not require.Equal): the roundtrip preserves byte CONTENT, and an
		// empty plaintext round-trips to a nil slice — the same bytes, which require.Equal
		// would (wrongly) treat as unequal.
		require.True(t, bytes.Equal(plaintext, got), "roundtrip must preserve bytes")
	})
}
