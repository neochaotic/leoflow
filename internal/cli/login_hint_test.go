package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestHintEmailUsername(t *testing.T) {
	auth401 := fmt.Errorf("server returned 401: invalid credentials")

	// A bare username + a 401 gets the e-mail hint.
	if got := hintEmailUsername("admin", auth401); !strings.Contains(got.Error(), "e-mail") {
		t.Errorf("bare username + 401 should hint at the e-mail form; got %v", got)
	}
	// An e-mail username is already correct — no hint.
	if got := hintEmailUsername("admin@leoflow.local", auth401); strings.Contains(got.Error(), "hint:") {
		t.Errorf("e-mail username should not get a hint; got %v", got)
	}
	// A non-401 error (e.g. network) must not get the credential hint.
	netErr := fmt.Errorf("posting to x: connection refused")
	if got := hintEmailUsername("admin", netErr); strings.Contains(got.Error(), "hint:") {
		t.Errorf("non-401 error should not get the hint; got %v", got)
	}
	// A nil error stays nil; an empty username gets nothing.
	if hintEmailUsername("admin", nil) != nil {
		t.Error("nil error must stay nil")
	}
	if got := hintEmailUsername("", auth401); strings.Contains(got.Error(), "hint:") {
		t.Error("empty username should not get a hint")
	}
}
