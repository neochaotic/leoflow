package version

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGetReturnsLinkTimeDefaults(t *testing.T) {
	got := Get()
	if got.Version != "dev" {
		t.Errorf("Version = %q, want %q", got.Version, "dev")
	}
	if got.GitCommit != "none" {
		t.Errorf("GitCommit = %q, want %q", got.GitCommit, "none")
	}
	if got.BuildDate != "unknown" {
		t.Errorf("BuildDate = %q, want %q", got.BuildDate, "unknown")
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestExpiryStatus(t *testing.T) {
	orig := expiresAt
	t.Cleanup(func() { expiresAt = orig })

	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	t.Run("no expiry baked in never expires", func(t *testing.T) {
		expiresAt = ""
		set, _, expired := ExpiryStatus(now)
		if set || expired {
			t.Errorf("set=%v expired=%v, want both false for empty expiresAt", set, expired)
		}
	})

	t.Run("future expiry is set but not expired", func(t *testing.T) {
		expiresAt = "2026-08-23T00:00:00Z"
		set, at, expired := ExpiryStatus(now)
		if !set || expired {
			t.Errorf("set=%v expired=%v, want set && !expired", set, expired)
		}
		if !at.Equal(time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("at = %v, want 2026-08-23", at)
		}
	})

	t.Run("past expiry is expired", func(t *testing.T) {
		expiresAt = "2026-05-01T00:00:00Z"
		set, _, expired := ExpiryStatus(now)
		if !set || !expired {
			t.Errorf("set=%v expired=%v, want set && expired", set, expired)
		}
	})

	t.Run("unparseable expiry is ignored", func(t *testing.T) {
		expiresAt = "not-a-date"
		set, _, expired := ExpiryStatus(now)
		if set || expired {
			t.Errorf("set=%v expired=%v, want both false for bad expiresAt", set, expired)
		}
	})
}

func TestInfoStringContainsEveryField(t *testing.T) {
	info := Info{
		Version:   "v1.2.3",
		GitCommit: "abc1234",
		BuildDate: "2026-05-21T00:00:00Z",
		GoVersion: "go1.26.1",
	}
	s := info.String()
	for _, want := range []string{info.Version, info.GitCommit, info.BuildDate, info.GoVersion} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}
