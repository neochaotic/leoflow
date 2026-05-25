package setup

import (
	"strings"
	"testing"
)

func TestResolvePython(t *testing.T) {
	cases := []struct {
		name               string
		goos, goarch, libc string
		wantTriple         string
		wantSHA            string
		wantErr            bool
	}{
		{"darwin arm64", "darwin", "arm64", "", "aarch64-apple-darwin", "03bcedae9b19a48888d7dc8ba064f73f6efaaf2b13f6a8e1a1bcc062df13e855", false},
		{"darwin amd64", "darwin", "amd64", "", "x86_64-apple-darwin", "5e388e3db8b59c8487ddd1423330b90fc7f0c6ef7eadec945441a180d0dd4bc4", false},
		{"linux amd64 glibc", "linux", "amd64", "glibc", "x86_64-unknown-linux-gnu", "14b5843a3492925dab6fdb7cca7d09af83ddf1fe2851f72cf9b1edc8ed2b1db7", false},
		{"linux arm64 glibc", "linux", "arm64", "glibc", "aarch64-unknown-linux-gnu", "0bc1b7acbb888881addf3a1c887a47d510d4300db6e3ad2ba461154b982e456a", false},
		{"linux amd64 musl", "linux", "amd64", "musl", "x86_64-unknown-linux-musl", "2135e9d2f1dea4ba21cf7ec441aa9c7b17fd551819dc8175598efa0fef5012ff", false},
		{"linux arm64 musl", "linux", "arm64", "musl", "aarch64-unknown-linux-musl", "185a6eab2578bd233c11bb1063374be447f559946444628fba2a519639aa5b08", false},
		// libc empty on linux defaults to glibc.
		{"linux amd64 unspecified libc", "linux", "amd64", "", "x86_64-unknown-linux-gnu", "14b5843a3492925dab6fdb7cca7d09af83ddf1fe2851f72cf9b1edc8ed2b1db7", false},
		{"unsupported os", "windows", "amd64", "", "", "", true},
		{"unsupported arch", "linux", "mips", "glibc", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := ResolvePython(c.goos, c.goarch, c.libc)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ResolvePython(%s,%s,%s) err = nil, want error", c.goos, c.goarch, c.libc)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePython err = %v, want nil", err)
			}
			wantSuffix := c.wantTriple + "-install_only.tar.gz"
			if !strings.HasSuffix(b.URL, wantSuffix) {
				t.Errorf("URL = %q, want suffix %q", b.URL, wantSuffix)
			}
			if !strings.HasPrefix(b.URL, "https://github.com/astral-sh/python-build-standalone/releases/download/") {
				t.Errorf("URL = %q, want python-build-standalone download prefix", b.URL)
			}
			if b.SHA256 != c.wantSHA {
				t.Errorf("SHA256 = %q, want %q", b.SHA256, c.wantSHA)
			}
		})
	}
}
