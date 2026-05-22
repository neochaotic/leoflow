package api

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedOpenAPIMatchesDocs guards against drift between the spec embedded
// in this package and the canonical docs/api/openapi.yaml.
func TestEmbeddedOpenAPIMatchesDocs(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading canonical spec: %v", err)
	}
	if !bytes.Equal(openAPISpec, canonical) {
		t.Error("embedded openapi.yaml differs from docs/api/openapi.yaml; re-copy it")
	}
}
