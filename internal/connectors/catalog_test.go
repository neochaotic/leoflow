package connectors

import (
	"strings"
	"testing"
)

// TestCatalogShape guards that every generated entry carries the identity the
// consumers need: a conn_type and a hook name. Provider-backed entries (those
// with a pip package) must name it the way PyPI ships it. Core types (generic,
// email) legitimately have no package.
func TestCatalogShape(t *testing.T) {
	cat := Catalog()
	if len(cat) < 10 {
		t.Fatalf("catalog too small: %d (did scripts/gen_connectors.py run against the full provider set?)", len(cat))
	}
	seen := map[string]bool{}
	for _, c := range cat {
		if c.ConnectionType == "" || c.HookName == "" {
			t.Errorf("incomplete connector: %+v", c)
		}
		if c.PipPackage != "" && !strings.HasPrefix(c.PipPackage, "apache-airflow-providers-") {
			t.Errorf("%s: pip package %q lacks the provider prefix", c.ConnectionType, c.PipPackage)
		}
		if seen[c.ConnectionType] {
			t.Errorf("duplicate connector type %q", c.ConnectionType)
		}
		seen[c.ConnectionType] = true
	}
}

// TestPackageForKnownAndUnknown pins the non-obvious package names (the ones a
// naive "join the dotted path" rule would get wrong) and the unknown path.
func TestPackageForKnownAndUnknown(t *testing.T) {
	cases := map[string]string{
		"postgres":              "apache-airflow-providers-postgres",
		"mssql":                 "apache-airflow-providers-microsoft-mssql",
		"aws":                   "apache-airflow-providers-amazon",
		"google_cloud_platform": "apache-airflow-providers-google",
	}
	for connType, want := range cases {
		got, ok := PackageFor(connType)
		if !ok || got != want {
			t.Errorf("PackageFor(%q) = (%q,%v), want (%q,true)", connType, got, ok, want)
		}
	}
	if got, ok := PackageFor("definitely-not-a-connector"); ok || got != "" {
		t.Errorf("PackageFor(unknown) = (%q,%v), want (\"\",false)", got, ok)
	}
}

// TestSugarAliases pins the ergonomic overlay: "gcp"/"google" resolve to the same
// provider as the canonical "google_cloud_platform". The canonical name keeps
// working — the alias is additive.
func TestSugarAliases(t *testing.T) {
	for _, name := range []string{"gcp", "google", "google_cloud_platform"} {
		got, ok := PackageFor(name)
		if !ok || got != "apache-airflow-providers-google" {
			t.Errorf("PackageFor(%q) = (%q,%v), want google provider", name, got, ok)
		}
	}
}

// TestResolve pins the sugar expansion: known names → packages (order preserved),
// unknown names collected separately for an actionable compile error.
func TestResolve(t *testing.T) {
	pkgs, unknown := Resolve([]string{"postgres", "http", "nope"})
	wantPkgs := []string{"apache-airflow-providers-postgres", "apache-airflow-providers-http"}
	if strings.Join(pkgs, ",") != strings.Join(wantPkgs, ",") {
		t.Errorf("packages = %v, want %v", pkgs, wantPkgs)
	}
	if strings.Join(unknown, ",") != "nope" {
		t.Errorf("unknown = %v, want [nope]", unknown)
	}
	if p, u := Resolve(nil); len(p) != 0 || len(u) != 0 {
		t.Errorf("Resolve(nil) = (%v,%v), want empty", p, u)
	}
}

// TestTypesAreSugarResolvable guards that Types() lists only pip-backed names
// (the ones a `connectors:` entry can actually install) and includes a known one.
func TestTypesListed(t *testing.T) {
	types := Types()
	found := false
	for _, ty := range types {
		if pkg, ok := PackageFor(ty); !ok || pkg == "" {
			t.Errorf("Types() listed %q which is not sugar-resolvable", ty)
		}
		if ty == "postgres" {
			found = true
		}
	}
	if !found {
		t.Error("Types() missing postgres")
	}
}

// TestExtraFieldsPopulatedForCloud is the ADR 0038 field-fidelity guard: a
// credential-rich connector (snowflake) must carry provider-specific extra_fields
// in the generated catalog — the gap the empty {} used to leave (ADR 0036).
func TestExtraFieldsPopulatedForCloud(t *testing.T) {
	for _, c := range Catalog() {
		if c.ConnectionType == "snowflake" {
			if len(c.ExtraFields) == 0 {
				t.Error("snowflake has no extra_fields; the generated form would miss account/warehouse/etc.")
			}
			return
		}
	}
	t.Skip("snowflake not in catalog (provider not installed at generation time)")
}
