package cli

import (
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// deprecatedPythonLine returns a python_version the schema marks deprecated, or
// skips the test. Hard-coding "3.10" here would turn every case below into a
// failure on the day the line is finally dropped, which is the one day nobody
// wants to be debugging the test suite.
func deprecatedPythonLine(t *testing.T) domain.PythonDeprecation {
	t.Helper()
	versions, err := domain.SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	for _, v := range versions {
		if d, ok := domain.DeprecatedPythonVersion(v); ok {
			return d
		}
	}
	t.Skip("no python_version is currently deprecated")
	return domain.PythonDeprecation{}
}

// supportedPythonLine returns a python_version with no deprecation note.
func supportedPythonLine(t *testing.T) string {
	t.Helper()
	versions, err := domain.SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	for _, v := range versions {
		if _, ok := domain.DeprecatedPythonVersion(v); !ok {
			return v
		}
	}
	t.Fatal("every supported python_version is deprecated")
	return ""
}

func TestWarnDeprecatedPythonDefaultPath(t *testing.T) {
	d := deprecatedPythonLine(t)
	var out strings.Builder
	if err := warnDeprecatedPython(&out, &domain.LeoflowConfig{PythonVersion: d.Version}); err != nil {
		t.Fatalf("warnDeprecatedPython: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"warning:",
		"python_version " + d.Version,
		publishedBaseRepo + ":py" + d.Version,
		d.RemoveAfter,
		`python_version: "` + d.Replacement + `"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning does not mention %q:\n%s", want, got)
		}
	}
}

func TestWarnDeprecatedPythonSilentOnSupported(t *testing.T) {
	var out strings.Builder
	if err := warnDeprecatedPython(&out, &domain.LeoflowConfig{PythonVersion: supportedPythonLine(t)}); err != nil {
		t.Fatalf("warnDeprecatedPython: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("a supported python_version produced a warning:\n%s", out.String())
	}
}

// A base_image pointing at somebody else's registry means python_version is not
// what the FROM is built from, so warning about it would be wrong and would
// train the author to filter this message out.
func TestWarnDeprecatedPythonSilentOnForeignBaseImage(t *testing.T) {
	d := deprecatedPythonLine(t)
	var out strings.Builder
	cfg := &domain.LeoflowConfig{PythonVersion: d.Version, BaseImage: "registry.example.com/team/base:2026-09"}
	if err := warnDeprecatedPython(&out, cfg); err != nil {
		t.Fatalf("warnDeprecatedPython: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("an unrelated base_image produced a warning:\n%s", out.String())
	}
}

// The population the deprecation is actually about: a pin to our own published
// tag is what keeps a user on a base after we stop rebuilding it. Both published
// tag shapes must be recognized.
func TestWarnDeprecatedPythonPinnedPublishedBase(t *testing.T) {
	d := deprecatedPythonLine(t)
	for name, ref := range map[string]string{
		"moving line tag": publishedBaseRepo + ":py" + d.Version,
		"per-release pin": publishedBaseRepo + ":py" + d.Version + "-v0.4.5",
		"per-release rc":  publishedBaseRepo + ":py" + d.Version + "-v0.4.5-rc.1",
	} {
		var out strings.Builder
		if err := warnDeprecatedPython(&out, &domain.LeoflowConfig{BaseImage: ref}); err != nil {
			t.Fatalf("%s: warnDeprecatedPython: %v", name, err)
		}
		if !strings.Contains(out.String(), "python_version "+d.Version) {
			t.Errorf("%s (%s) produced no warning:\n%s", name, ref, out.String())
		}
	}
}

func TestWarnDeprecatedPythonSilentOnSupportedPin(t *testing.T) {
	ref := publishedBaseRepo + ":py" + supportedPythonLine(t)
	var out strings.Builder
	if err := warnDeprecatedPython(&out, &domain.LeoflowConfig{BaseImage: ref}); err != nil {
		t.Fatalf("warnDeprecatedPython: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("a pin to a supported line produced a warning:\n%s", out.String())
	}
}

func TestWarnDeprecatedPythonNilAndEmpty(t *testing.T) {
	var out strings.Builder
	if err := warnDeprecatedPython(&out, nil); err != nil {
		t.Fatalf("nil config: %v", err)
	}
	if err := warnDeprecatedPython(&out, &domain.LeoflowConfig{}); err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("nil/empty config produced a warning:\n%s", out.String())
	}
}

func TestWrapWords(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want []string
	}{
		{"empty", "   ", 10, nil},
		{"fits on one line", "a b c", 10, []string{"a b c"}},
		{"folds at the boundary", "aaaa bbbb cccc", 9, []string{"aaaa bbbb", "cccc"}},
		{"collapses runs of whitespace", "a \n  b", 10, []string{"a b"}},
		{
			// An image reference longer than the column budget must survive intact:
			// a wrapped one cannot be copy-pasted, which is the only thing the
			// reader wants to do with it.
			"an over-long token is not cut",
			"pull ghcr.io/neochaotic/leoflow-runtime:py3.10-v0.4.5 now",
			20,
			[]string{"pull", "ghcr.io/neochaotic/leoflow-runtime:py3.10-v0.4.5", "now"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapWords(tc.in, tc.cols)
			if len(got) != len(tc.want) {
				t.Fatalf("wrapWords(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("wrapWords(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
				}
			}
		})
	}
}
