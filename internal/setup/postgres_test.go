package setup

import (
	"strings"
	"testing"
)

// TestResolvePostgres covers the platform/libc → relocatable Postgres asset
// mapping (theseus-rs/postgresql-binaries), including the musl variants that let
// Lite run an own-managed Postgres without Docker on Alpine too.
func TestResolvePostgres(t *testing.T) {
	cases := []struct {
		goos, goarch, libc string
		wantTriple         string
	}{
		{"darwin", "arm64", "", "aarch64-apple-darwin"},
		{"darwin", "amd64", "", "x86_64-apple-darwin"},
		{"linux", "amd64", "", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "", "aarch64-unknown-linux-gnu"},
		{"linux", "amd64", "musl", "x86_64-unknown-linux-musl"},
		{"linux", "arm64", "musl", "aarch64-unknown-linux-musl"},
	}
	for _, c := range cases {
		b, err := ResolvePostgres(c.goos, c.goarch, c.libc)
		if err != nil {
			t.Errorf("%s/%s libc=%q: unexpected error %v", c.goos, c.goarch, c.libc, err)
			continue
		}
		wantAsset := "postgresql-" + pgVersion + "-" + c.wantTriple + ".tar.gz"
		if got := b.URL; !strings.Contains(got, wantAsset) || !strings.Contains(got, pgBaseURL) {
			t.Errorf("%s/%s libc=%q: URL=%q, want it to contain %q", c.goos, c.goarch, c.libc, got, wantAsset)
		}
		if len(b.SHA256) != 64 {
			t.Errorf("%s/%s libc=%q: SHA256=%q, want a 64-hex digest", c.goos, c.goarch, c.libc, b.SHA256)
		}
		if b.Version != pgVersion {
			t.Errorf("version = %q, want %q", b.Version, pgVersion)
		}
	}

	// Windows is unsupported (WSL2 is the path), as is an unknown arch.
	if _, err := ResolvePostgres("windows", "amd64", ""); err == nil {
		t.Error("windows should be unsupported")
	}
	if _, err := ResolvePostgres("linux", "riscv64", ""); err == nil {
		t.Error("unknown arch should error")
	}
}
