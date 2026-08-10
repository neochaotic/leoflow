package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewInjectsBearerToken: a client built with a token carries
// "Authorization: Bearer <token>" on every request and targets the /api/v2 path
// the operation maps to. This is the pass-through auth the MCP and CLI rely on
// (ADR 0050 D9) — the client never mints a token, it forwards the caller's.
func TestNewInjectsBearerToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[],"total_entries":0}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "tok123")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListDagsWithResponse(context.Background(), nil); err != nil {
		t.Fatalf("ListDags: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
	}
	if gotPath != "/api/v2/dags" {
		t.Errorf("path = %q, want /api/v2/dags", gotPath)
	}
}

// TestNewOmitsAuthHeaderWithoutToken: an empty token leaves requests
// unauthenticated (dev/loopback), rather than sending an empty bearer.
func TestNewOmitsAuthHeaderWithoutToken(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if hadAuth {
		t.Error("no token must send no Authorization header")
	}
}

// TestNewTrimsTrailingSlash: a base URL with a trailing slash must not produce
// a double slash in the request path.
func TestNewTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if gotPath != "/healthz" {
		t.Errorf("path = %q, want /healthz (no double slash)", gotPath)
	}
}
