package domain

import (
	"errors"
	"strings"
	"testing"
)

func dbtCfg(project string) *LeoflowConfig {
	return &LeoflowConfig{
		SchemaVersion: "1.0",
		DagID:         "sales",
		Dbt:           &DbtConfig{Project: project, Schedule: "@daily"},
	}
}

// dbt.project is resolved with filepath.Join(dagDir, project) and, for a Pro
// image build, baked into the image at that relative path. Both break for a path
// that is absolute or leaves the DAG directory — and they break silently, which
// is the part worth catching.
func TestValidateRejectsUnusableDbtProject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		wants   string
	}{
		// filepath.Join("/dags/sales", "/opt/dbt/proj") is
		// "/dags/sales/opt/dbt/proj" — Join does not treat an absolute second
		// element specially, so the leading slash is simply swallowed.
		{"absolute", "/opt/dbt/proj", "absolute"},
		{"absolute root", "/", "absolute"},
		// filepath.Join("/dags/sales", "../../../etc") is "/etc": outside the DAG
		// directory, so outside the Docker build context too.
		{"escapes the dag directory", "../../../etc", "outside"},
		{"escapes one level", "../sibling", "outside"},
		{"escapes after descending", "transform/../../elsewhere", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dbtCfg(tc.project).Validate()
			if err == nil {
				t.Fatalf("Validate accepted dbt.project = %q", tc.project)
			}
			if !errors.Is(err, ErrInvalidDbtProject) {
				t.Fatalf("error does not wrap ErrInvalidDbtProject: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should explain the problem (%q)", err, tc.wants)
			}
			if !strings.Contains(err.Error(), tc.project) {
				t.Errorf("error %q should quote the offending value", err)
			}
		})
	}
}

// The ordinary forms must keep working, including the ones a dbt user writes.
func TestValidateAcceptsUsableDbtProject(t *testing.T) {
	for _, project := range []string{
		"",                       // no dbt project declared
		".",                      // the DAG directory itself
		"transform",              // the common case
		"./transform",            // the same, written explicitly
		"dbt/transform",          // nested
		"transform/../analytics", // normalises to "analytics", still inside
	} {
		t.Run("project="+project, func(t *testing.T) {
			if err := dbtCfg(project).Validate(); err != nil {
				t.Errorf("rejected a usable dbt.project %q: %v", project, err)
			}
		})
	}
}

// dbt.manifest goes through the same filepath.Join as project
// (dbt_manifest.go:26 joins it onto the already-joined project dir), so an
// absolute manifest path is mangled the same way and needs the same rejection.
// The first version of this validation checked only project, which left half the
// reported bug in place.
func TestValidateRejectsUnusableDbtManifest(t *testing.T) {
	for _, tc := range []struct{ name, manifest, wants string }{
		{"absolute", "/tmp/proj/target/manifest.json", "absolute"},
		{"escapes", "../../../etc/passwd", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dbtCfg("transform")
			cfg.Dbt.Manifest = tc.manifest
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted dbt.manifest = %q", tc.manifest)
			}
			if !errors.Is(err, ErrInvalidDbtProject) {
				t.Fatalf("error does not wrap ErrInvalidDbtProject: %v", err)
			}
			if !strings.Contains(err.Error(), "manifest") {
				t.Errorf("error %q should name the manifest field, not just the project", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should explain the problem (%q)", err, tc.wants)
			}
		})
	}
}

func TestValidateAcceptsUsableDbtManifest(t *testing.T) {
	for _, m := range []string{"", "target/manifest.json", "./target/manifest.json"} {
		cfg := dbtCfg("transform")
		cfg.Dbt.Manifest = m
		if err := cfg.Validate(); err != nil {
			t.Errorf("rejected a usable dbt.manifest %q: %v", m, err)
		}
	}
}
