package cli

import (
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestReleaseBaseTag(t *testing.T) {
	cases := map[string]string{
		"v0.4.2":                  "v0.4.2",      // clean release tag → pin
		"v0.4.3-rc.1":             "v0.4.3-rc.1", // rc tag is still a release
		"v1.2.3-beta.4":           "v1.2.3-beta.4",
		"v0.4.2-9-gabc1234":       "", // git describe (commits after tag) → moving
		"v0.4.2-9-gabc1234-dirty": "", // describe + dirty
		"v0.4.2-dirty":            "", // dirty tree
		"dev":                     "", // default source build
		"":                        "", // unset
	}
	for in, want := range cases {
		if got := releaseBaseTag(in); got != want {
			t.Errorf("releaseBaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveBaseImage(t *testing.T) {
	// An explicit base_image always wins.
	if got := resolveBaseImage(&domain.LeoflowConfig{BaseImage: "my/base:tag", PythonVersion: "3.11"}); got != "my/base:tag" {
		t.Errorf("base_image override = %q, want my/base:tag", got)
	}
	// A dev/test build (version = "dev") pins the moving py<ver> tag — no versioned
	// base is published for a non-release build.
	got := resolveBaseImage(&domain.LeoflowConfig{PythonVersion: "3.11"})
	if got != publishedBaseRepo+":py3.11" {
		t.Errorf("dev build base = %q, want %q (moving tag)", got, publishedBaseRepo+":py3.11")
	}
	if strings.Contains(got, "-") && !strings.HasPrefix(got, publishedBaseRepo) {
		t.Errorf("unexpected version suffix on a dev build: %q", got)
	}
}
