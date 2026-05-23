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
