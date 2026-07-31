package secrets

import (
	"errors"
	"testing"
)

func key32() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewAESGCM(key32())
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "hunter2", "a longer secret with spaces & symbols #!"} {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if enc == plain && plain != "" {
			t.Errorf("ciphertext equals plaintext for %q", plain)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != plain {
			t.Errorf("round-trip = %q, want %q", got, plain)
		}
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	c, _ := NewAESGCM(key32())
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Error("same plaintext must not produce identical ciphertext (nonce reuse)")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	c1, _ := NewAESGCM(key32())
	other := key32()
	other[0] ^= 0xFF
	c2, _ := NewAESGCM(other)
	enc, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Error("decrypt with wrong key should fail (GCM auth)")
	}
}

func TestNewAESGCMRejectsBadKeyLen(t *testing.T) {
	if _, err := NewAESGCM([]byte("short")); err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey(""); !errors.Is(err, ErrNoKey) {
		t.Errorf("empty key = %v, want ErrNoKey", err)
	}
	if b, err := ParseKey("0123456789abcdef0123456789abcdef"); err != nil || len(b) != 32 {
		t.Errorf("raw 32-char key: len=%d err=%v", len(b), err)
	}
	// 64-char hex.
	if b, err := ParseKey("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"); err != nil || len(b) != 32 {
		t.Errorf("hex key: len=%d err=%v", len(b), err)
	}
	if _, err := ParseKey("too-short"); err == nil {
		t.Error("expected error for short key")
	}
}

// A 32-character key made entirely of hex digits is almost certainly the output
// of `openssl rand -hex 16` — the command the docs used to recommend. ParseKey
// accepts it as 32 RAW bytes, so the cipher is AES-256 over 128 bits of
// entropy, and nothing says so. The two shapes are indistinguishable by length,
// so this cannot be a rejection without also rejecting a legitimate 32-character
// passphrase; it is a warning the caller can surface.
func TestLooksLikeHalfEntropyHexKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want bool
	}{
		{"openssl rand -hex 16 output", "0123456789abcdef0123456789abcdef", true},
		{"uppercase hex is the same mistake", "0123456789ABCDEF0123456789ABCDEF", true},
		{"a real 32-char passphrase is not", "leoflow-unittest-secretkey-32by!", false},
		{"64-char hex is the correct form", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"short strings are someone else's problem", "abc", false},
		{"empty", "", false},
	} {
		if got := LooksLikeHalfEntropyHexKey(tc.key); got != tc.want {
			t.Errorf("%s: LooksLikeHalfEntropyHexKey(%q) = %v, want %v", tc.name, tc.key, got, tc.want)
		}
	}
}
