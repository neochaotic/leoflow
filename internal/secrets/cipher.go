// Package secrets encrypts sensitive values (e.g. connection passwords) at rest
// with AES-256-GCM under a configured key. See ADR 0019.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ErrNoKey reports that no encryption key is configured; callers must treat
// secret writes as unavailable rather than storing plaintext.
var ErrNoKey = errors.New("no encryption key configured")

// Cipher encrypts and decrypts secret values.
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// aesGCM is an AES-256-GCM Cipher. The stored form is
// base64(nonce || ciphertext || tag).
type aesGCM struct {
	aead cipher.AEAD
}

// NewAESGCM builds a Cipher from a 32-byte key. The key may be given raw
// (32 bytes), hex (64 chars), or base64 (standard or raw); ParseKey handles the
// decoding.
func NewAESGCM(key []byte) (Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("building gcm: %w", err)
	}
	return &aesGCM{aead: aead}, nil
}

// ParseKey decodes a configured key string into 32 raw bytes, accepting a raw
// 32-char string, 64-char hex, or base64 (standard or raw).
func ParseKey(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrNoKey
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("encryption key must decode to 32 bytes (raw, hex, or base64)")
}

// Encrypt seals plaintext with a fresh nonce, returning base64(nonce||sealed).
func (c *aesGCM) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (c *aesGCM) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, sealed := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}
	return string(plain), nil
}

// LooksLikeHalfEntropyHexKey reports whether a key is 32 characters of pure
// hexadecimal — the signature of `openssl rand -hex 16`, which the project's own
// docs recommended until they were corrected.
//
// ParseKey accepts a 32-character string as 32 RAW bytes, so such a key is used
// as an AES-256 key carrying 128 bits of entropy. The cipher is still AES-256;
// the secret behind it is half the size intended, and nothing about the
// configuration says so.
//
// This is a warning and not a rejection because the two shapes cannot be told
// apart with certainty: a legitimate 32-character passphrase made only of the
// characters 0-9a-f is unlikely but perfectly valid, and refusing it would break
// an operator who did nothing wrong. Reporting it lets the caller say something
// while still starting.
func LooksLikeHalfEntropyHexKey(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
