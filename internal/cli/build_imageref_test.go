package cli

import "testing"

func TestComposeImageRef(t *testing.T) {
	if got := composeImageRef("ghcr.io/org", "etl", "abc"); got != "ghcr.io/org/etl:abc" {
		t.Errorf("ref = %q, want ghcr.io/org/etl:abc", got)
	}
	// A trailing slash on the URL must not double up.
	if got := composeImageRef("ghcr.io/org/", "etl", "abc"); got != "ghcr.io/org/etl:abc" {
		t.Errorf("ref = %q, want a single slash join", got)
	}
}

func TestResolveImageTagByStrategy(t *testing.T) {
	cases := []struct {
		strategy, version, gitSHA, want string
	}{
		{"version", "v1", "sha9", "v1"},
		{"git_sha", "v1", "sha9", "sha9"},
		{"git-sha", "v1", "sha9", "sha9"},
		// Default (unset) prefers the immutable git sha...
		{"", "v1", "sha9", "sha9"},
		// ...but falls back to the version when not in git.
		{"", "v1", "", "v1"},
	}
	for _, c := range cases {
		if got := resolveImageTag(c.strategy, c.version, c.gitSHA); got != c.want {
			t.Errorf("resolveImageTag(%q,%q,%q) = %q, want %q",
				c.strategy, c.version, c.gitSHA, got, c.want)
		}
	}
}
