package connectors

import (
	"strings"
	"testing"
)

// TestCatalogShape guards that every curated connector carries the identity the
// three consumers need (the admin form, the `connectors:` sugar expansion, and
// compile validation): a type, a display name, an Airflow hook class, and a pip
// package named the way PyPI ships it.
func TestCatalogShape(t *testing.T) {
	cat := Catalog()
	if len(cat) < 10 {
		t.Fatalf("catalog too small: %d", len(cat))
	}
	seen := map[string]bool{}
	for _, c := range cat {
		if c.Type == "" || c.DisplayName == "" || c.HookClass == "" || c.PipPackage == "" {
			t.Errorf("incomplete connector: %+v", c)
		}
		if !strings.HasPrefix(c.PipPackage, "apache-airflow-providers-") {
			t.Errorf("%s: pip package %q lacks the provider prefix", c.Type, c.PipPackage)
		}
		if seen[c.Type] {
			t.Errorf("duplicate connector type %q", c.Type)
		}
		seen[c.Type] = true
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
		"kafka":                 "apache-airflow-providers-apache-kafka",
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

// TestSugarAliases pins the ergonomic aliases: the `connectors:` short names a
// user reaches for first. Airflow's own conn_types are asymmetric (`aws` is
// terse, `google_cloud_platform` is verbose), so the sugar accepts a friendly
// alias (`gcp`) that resolves to the same provider as the canonical conn_type.
// The canonical name keeps working — the alias is additive, not a rename.
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
	// Empty in → empty out (no spurious entries).
	if p, u := Resolve(nil); len(p) != 0 || len(u) != 0 {
		t.Errorf("Resolve(nil) = (%v,%v), want empty", p, u)
	}
}

// TestTypesListed lets callers build "known: postgres, mysql, …" error messages.
func TestTypesListed(t *testing.T) {
	types := Types()
	if len(types) != len(Catalog()) {
		t.Fatalf("Types() len %d != catalog len %d", len(types), len(Catalog()))
	}
	found := false
	for _, ty := range types {
		if ty == "postgres" {
			found = true
		}
	}
	if !found {
		t.Error("Types() missing postgres")
	}
}
