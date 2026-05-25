package main

import (
	"strings"
	"testing"
	"time"
)

func TestCheckServerExpiry(t *testing.T) {
	orig := serverExpiryStatus
	t.Cleanup(func() { serverExpiryStatus = orig })

	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	noEnv := func(string) string { return "" }
	at := func(rfc3339 string) time.Time {
		ts, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			t.Fatalf("bad test timestamp %q: %v", rfc3339, err)
		}
		return ts
	}

	t.Run("no expiry starts", func(t *testing.T) {
		serverExpiryStatus = func(time.Time) (bool, time.Time, bool) { return false, time.Time{}, false }
		if err := checkServerExpiry(now, noEnv); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("future expiry starts", func(t *testing.T) {
		exp := at("2027-01-01T00:00:00Z")
		serverExpiryStatus = func(now time.Time) (bool, time.Time, bool) { return true, exp, now.After(exp) }
		if err := checkServerExpiry(now, noEnv); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("expired refuses to start", func(t *testing.T) {
		exp := at("2026-05-01T00:00:00Z")
		serverExpiryStatus = func(now time.Time) (bool, time.Time, bool) { return true, exp, now.After(exp) }
		err := checkServerExpiry(now, noEnv)
		if err == nil {
			t.Fatal("err = nil, want expiry error")
		}
		if !strings.Contains(err.Error(), "expired on 2026-05-01") {
			t.Errorf("err = %q, want expiry message", err.Error())
		}
	})

	t.Run("LEOFLOW_IGNORE_EXPIRY overrides", func(t *testing.T) {
		exp := at("2026-05-01T00:00:00Z")
		serverExpiryStatus = func(now time.Time) (bool, time.Time, bool) { return true, exp, now.After(exp) }
		env := func(k string) string {
			if k == "LEOFLOW_IGNORE_EXPIRY" {
				return "1"
			}
			return ""
		}
		if err := checkServerExpiry(now, env); err != nil {
			t.Fatalf("err = %v, want nil when override set", err)
		}
	})
}
