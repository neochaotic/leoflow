package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteOpenMetadataGoldenBodies captures the four responses OpenMetadata's
// REST connector reads, so test/om-contract/validate.py can check them against
// OM's own pydantic models.
//
// Two layers are needed, not one. A Go test cannot prove that pydantic accepts a
// body: the #1149-era defect was a field that is valid JSON, valid against our
// OpenAPI spec, and rejected by OM's type. Equally, the Python layer cannot run
// the server. So Go writes the bodies and Python judges them.
//
// Writing is gated on LEOFLOW_OM_GOLDEN so a normal `go test` run stays
// read-only; the CI step sets it.
func TestWriteOpenMetadataGoldenBodies(t *testing.T) {
	dir := os.Getenv("LEOFLOW_OM_GOLDEN")
	if dir == "" {
		t.Skip("gate skipped: set LEOFLOW_OM_GOLDEN to the directory to write")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	srv := structureServer(&fakeSpecReader{spec: diamondSpec()})
	for _, c := range []struct{ file, path string }{
		{"tasks.json", "/api/v2/dags/etl/tasks"},
	} {
		rec := authGet(srv, http.MethodGet, c.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", c.path, rec.Code, rec.Body.String())
		}
		if !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("GET %s returned invalid JSON", c.path)
		}
		if err := os.WriteFile(filepath.Join(dir, c.file), rec.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	api := authedServer()
	for _, c := range []struct{ file, path string }{
		{"dags.json", "/api/v2/dags"},
		{"dag_runs.json", "/api/v2/dags/etl/dagRuns"},
		{"task_instances.json", "/api/v2/dags/etl/dagRuns/r1/taskInstances"},
	} {
		rec := authGet(api, http.MethodGet, c.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", c.path, rec.Code, rec.Body.String())
		}
		if err := os.WriteFile(filepath.Join(dir, c.file), rec.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
