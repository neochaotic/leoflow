package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestTokenReturnsAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"jwt-xyz","token_type":"bearer"}`)
	}))
	defer srv.Close()

	tok, err := requestToken(context.Background(), srv.URL, "admin", "pw")
	if err != nil {
		t.Fatalf("requestToken: %v", err)
	}
	if tok != "jwt-xyz" {
		t.Errorf("token = %q, want jwt-xyz", tok)
	}
}

func TestRequestTokenRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := requestToken(context.Background(), srv.URL, "admin", "wrong"); err == nil {
		t.Error("expected error on 401")
	}
}

func TestCreateTokenCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"jwt-abc"}`)
	}))
	defer srv.Close()

	out, _, err := run(t, "auth", "create-token", "--server", srv.URL, "--username", "a", "--password", "b")
	if err != nil {
		t.Fatalf("create-token: %v", err)
	}
	if !strings.Contains(out, "jwt-abc") {
		t.Errorf("output = %q, want to contain the token", out)
	}
}
