package version

import (
	"runtime"
	"strings"
	"testing"
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
