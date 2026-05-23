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
