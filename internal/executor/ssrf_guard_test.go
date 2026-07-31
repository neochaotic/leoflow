package executor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestDeniedIP(t *testing.T) {
	for _, tc := range []struct {
		ip     string
		denied bool
	}{
		{"169.254.169.254", true}, // cloud metadata (link-local) — the headline SSRF target
		{"127.0.0.1", true},       // loopback
		{"::1", true},             // loopback v6
		{"10.0.0.5", true},        // RFC1918
		{"172.16.3.4", true},      // RFC1918
		{"192.168.1.1", true},     // RFC1918
		{"fd00::1", true},         // IPv6 unique-local
		{"fe80::1", true},         // IPv6 link-local
		{"0.0.0.0", true},         // unspecified
		{"8.8.8.8", false},        // public
		{"1.1.1.1", false},        // public
		{"93.184.216.34", false},  // public (example.com)
	} {
		if got := deniedIP(net.ParseIP(tc.ip)); got != tc.denied {
			t.Errorf("deniedIP(%s) = %v, want %v", tc.ip, got, tc.denied)
		}
	}
}

func TestGuardedDialControlBlocksPrivate(t *testing.T) {
	if err := guardedDialControl("tcp", "169.254.169.254:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("metadata IP should be blocked, got %v", err)
	}
	if err := guardedDialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("a public IP should be allowed, got %v", err)
	}
}

func TestAllowedSchemeRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "gopher://x", "ftp://h/f", "s3://b/k"} {
		if err := allowedScheme(u); !errors.Is(err, ErrBlockedScheme) {
			t.Errorf("%s should be blocked, got %v", u, err)
		}
	}
	for _, u := range []string{"http://example.com", "https://api.example.com/x"} {
		if err := allowedScheme(u); err != nil {
			t.Errorf("%s should be allowed, got %v", u, err)
		}
	}
}

// The default (production) executor's client refuses to reach a loopback server:
// the SSRF guard is wired into the client the control plane actually uses.
func TestInlineHTTPDefaultClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewInlineHTTPExecutor(nil, 0) // nil = the guarded default
	_, err := exec.Run(context.Background(), Request{
		HTTPRequest: &domain.HTTPRequest{Method: "GET", URL: srv.URL},
	})
	if err == nil {
		t.Fatal("the guarded default client must not reach a loopback address")
	}
}

// A non-http scheme is rejected before any request is made, regardless of client.
func TestInlineHTTPRejectsNonHTTPScheme(t *testing.T) {
	exec := NewInlineHTTPExecutor(&http.Client{}, 0)
	_, err := exec.Run(context.Background(), Request{
		HTTPRequest: &domain.HTTPRequest{Method: "GET", URL: "file:///etc/passwd"},
	})
	if !errors.Is(err, ErrBlockedScheme) {
		t.Errorf("a file:// URL must be rejected, got %v", err)
	}
}

// The error paths: a malformed dial address and an unparseable URL. Covering
// them keeps the guard's coverage complete (the SSRF guard sits on the razor
// edge of the aggregate floor, so its own lines must be fully exercised).
func TestGuardedDialControlMalformedAddress(t *testing.T) {
	if err := guardedDialControl("tcp", "no-port-here", nil); err == nil {
		t.Error("an address without host:port should error")
	}
	// A non-IP host at the control stage (should not happen post-resolution) is denied.
	if err := guardedDialControl("tcp", "example.com:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("a non-IP host must be blocked (fail closed), got %v", err)
	}
}

func TestAllowedSchemeUnparseable(t *testing.T) {
	if err := allowedScheme("://missing-scheme"); !errors.Is(err, ErrBlockedScheme) {
		t.Errorf("an unparseable url must be blocked, got %v", err)
	}
}
