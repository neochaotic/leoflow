package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestEnforceExpiry(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	t.Run("no expiry baked in passes silently", func(t *testing.T) {
		var buf bytes.Buffer
		if err := enforceExpiry(now, "dev", &buf); err != nil {
			t.Fatalf("err = %v, want nil for dev build", err)
		}
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want empty", buf.String())
		}
	})
}

func TestEnforceExpiryWithBakedExpiry(t *testing.T) {
	orig := expiryStatus
	t.Cleanup(func() { expiryStatus = orig })

	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	setExpiresAtForTest := func(rfc3339 string) {
		at, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			t.Fatalf("bad test timestamp %q: %v", rfc3339, err)
		}
		expiryStatus = func(now time.Time) (bool, time.Time, bool) {
			return true, at, now.After(at)
		}
	}

	t.Run("far future is silent", func(t *testing.T) {
		setExpiresAtForTest("2027-01-01T00:00:00Z")
		var buf bytes.Buffer
		if err := enforceExpiry(now, "dev", &buf); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if buf.Len() != 0 {
			t.Errorf("stderr = %q, want empty (outside warn window)", buf.String())
		}
	})

	t.Run("within warn window warns but proceeds", func(t *testing.T) {
		setExpiresAtForTest("2026-05-30T00:00:00Z")
		var buf bytes.Buffer
		if err := enforceExpiry(now, "dev", &buf); err != nil {
			t.Fatalf("err = %v, want nil within warn window", err)
		}
		if !strings.Contains(buf.String(), "expires on 2026-05-30") {
			t.Errorf("stderr = %q, want expiry warning", buf.String())
		}
	})

	t.Run("expired blocks a normal command", func(t *testing.T) {
		setExpiresAtForTest("2026-05-01T00:00:00Z")
		var buf bytes.Buffer
		err := enforceExpiry(now, "dev", &buf)
		if err == nil {
			t.Fatal("err = nil, want expiry error")
		}
		if !strings.Contains(err.Error(), "expired on 2026-05-01") {
			t.Errorf("err = %q, want expiry message", err.Error())
		}
	})

	t.Run("expired still lets version run", func(t *testing.T) {
		setExpiresAtForTest("2026-05-01T00:00:00Z")
		var buf bytes.Buffer
		if err := enforceExpiry(now, "version", &buf); err != nil {
			t.Fatalf("err = %v, want nil for exempt command", err)
		}
		if !strings.Contains(buf.String(), "expired on 2026-05-01") {
			t.Errorf("stderr = %q, want 'running anyway' notice", buf.String())
		}
	})

	t.Run("LEOFLOW_IGNORE_EXPIRY overrides", func(t *testing.T) {
		setExpiresAtForTest("2026-05-01T00:00:00Z")
		t.Setenv(expiryIgnoreEnv, "1")
		var buf bytes.Buffer
		if err := enforceExpiry(now, "dev", &buf); err != nil {
			t.Fatalf("err = %v, want nil when override set", err)
		}
	})
}
