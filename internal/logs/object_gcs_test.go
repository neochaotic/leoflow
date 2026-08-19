package logs

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestNewGCSStoreRequiresBucket(t *testing.T) {
	if _, err := NewGCSStore(context.Background(), GCSConfig{}); err == nil {
		t.Fatal("NewGCSStore with empty bucket = nil error, want error")
	}
}

// TestNewGCSStoreBuildsClient constructs a store with a well-formed (but throwaway)
// service-account credentials file, asserting the constructor wires the bucket and
// satisfies ObjectStore. A service-account token source is built lazily, so this
// makes no network call. The keyless (ADC) path is not exercised here because it
// requires ambient GKE Workload Identity — the memStore tests in object_test.go
// cover the provider-agnostic Put/Get behavior instead.
func TestNewGCSStoreBuildsClient(t *testing.T) {
	credFile := writeFakeServiceAccount(t)
	store, err := NewGCSStore(context.Background(), GCSConfig{
		Bucket:          "task-logs",
		CredentialsFile: credFile,
	})
	if err != nil {
		t.Fatalf("NewGCSStore() error = %v", err)
	}
	var _ ObjectStore = store
	if store.bucket != "task-logs" {
		t.Errorf("store.bucket = %q, want \"task-logs\"", store.bucket)
	}
	if store.client == nil {
		t.Error("store.client = nil, want a configured GCS client")
	}
}

// writeFakeServiceAccount emits a syntactically valid service-account JSON with a
// freshly generated RSA key to a temp file. It is enough for the SDK to build a
// JWT token source offline; it authenticates against nothing.
func writeFakeServiceAccount(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
	sa := map[string]string{
		"type":                        "service_account",
		"project_id":                  "test-project",
		"private_key_id":              "0000000000000000000000000000000000000000",
		"private_key":                 pemKey,
		"client_email":                "test@test-project.iam.gserviceaccount.com",
		"client_id":                   "000000000000000000000",
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
	}
	blob, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshaling fake SA: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("writing fake SA: %v", err)
	}
	return path
}
