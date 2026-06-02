package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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

// TestDocsHandlersServeKnownContent locks the three /docs surfaces — Scalar
// HTML, raw YAML, JSON-coerced — so a regression in the embedded spec or the
// YAML→JSON pipe is caught at unit time (it would otherwise only surface to a
// browser hitting /docs).
func TestDocsHandlersServeKnownContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDocs(r)

	t.Run("/docs serves Scalar HTML", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/docs", http.NoBody)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), "api-reference") {
			t.Error("response missing the Scalar reference script tag")
		}
	})

	t.Run("/openapi.yaml serves the embedded spec verbatim", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/openapi.yaml", http.NoBody)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), openAPISpec) {
			t.Error("/openapi.yaml body differs from the embedded spec")
		}
	})

	t.Run("/openapi.json transcodes the YAML spec", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/openapi.json", http.NoBody)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		if _, ok := doc["openapi"]; !ok {
			t.Error("decoded JSON is missing the top-level 'openapi' key")
		}
	})
}
