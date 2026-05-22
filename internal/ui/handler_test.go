package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func fixture() *Server {
	return NewFromFS(fstest.MapFS{
		"index.html":           {Data: []byte(`<base href="{{ backend_server_base_url }}" /><div id="root"></div>`)},
		"assets/app-abc123.js": {Data: []byte("console.log('hi')")},
		"sql_bg-xyz.wasm":      {Data: []byte("\x00asm")},
		"VERSION":              {Data: []byte("3.2.1\n")},
	}, "3.2.1")
}

func TestIndexTemplatesBaseHref(t *testing.T) {
	rec := httptest.NewRecorder()
	fixture().Index(rec, "/")
	body := rec.Body.String()
	if strings.Contains(body, baseHrefPlaceholder) {
		t.Errorf("base href placeholder not substituted: %q", body)
	}
	if !strings.Contains(body, `<base href="/" />`) {
		t.Errorf("expected templated base href, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index cache-control = %q, want no-cache", cc)
	}
}

func TestIndexDefaultsEmptyBasePathToRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	fixture().Index(rec, "")
	if !strings.Contains(rec.Body.String(), `<base href="/" />`) {
		t.Errorf("empty base path should default to /, got %q", rec.Body.String())
	}
}

func TestStaticServesHashedAssetImmutable(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app-abc123.js", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static asset = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset cache-control = %q, want immutable", cc)
	}
}

func TestStaticServesWasmWithCorrectMIME(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/sql_bg-xyz.wasm", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wasm = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/wasm") {
		t.Errorf("wasm content-type = %q, want application/wasm", ct)
	}
}

func TestStaticMissingFileIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/nope.js", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing static file = %d, want 404 (no SPA fallback under /static)", rec.Code)
	}
}

func TestEmbeddedBundleHasIndex(t *testing.T) {
	// The committed placeholder (or fetched bundle) must always carry index.html
	// so the binary builds and serves a shell.
	rec := httptest.NewRecorder()
	New().Index(rec, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "root") {
		t.Errorf("embedded index = %d, body=%q", rec.Code, rec.Body.String())
	}
}
