package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// writeDagJSON writes a minimal two-task dag.json to a temp file and returns its
// path, for exercising the compile overlay in isolation.
func writeDagJSON(t *testing.T) string {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "proj",
		DagVersion:    "dev",
		Image:         "test:v1",
		Tasks: []domain.TaskSpec{
			{TaskID: "extract", Type: domain.TaskTypePython, Entrypoint: "dag:extract"},
			{TaskID: "transform", Type: domain.TaskTypePython, Entrypoint: "dag:transform"},
		},
	}
	data, err := json.Marshal(&spec)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "dag.json")
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		t.Fatalf("write fixture: %v", werr)
	}
	return path
}

// readSpec loads a dag.json from disk for assertions.
func readSpec(t *testing.T, path string) domain.DAGSpec {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled temp path.
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatalf("unmarshal spec: %v", uerr)
	}
	return spec
}

func taskByID(spec domain.DAGSpec, id string) (domain.TaskSpec, bool) {
	for _, ts := range spec.Tasks {
		if ts.TaskID == id {
			return ts, true
		}
	}
	return domain.TaskSpec{}, false
}

func TestOverlayProjectAppliesTaskOverrides(t *testing.T) {
	path := writeDagJSON(t)
	retries := 5
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{
			"transform": {
				Retries:   &retries,
				Env:       map[string]string{"TZ": "UTC"},
				Resources: &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "2", Memory: "4Gi"}},
			},
		},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)

	transform, ok := taskByID(got, "transform")
	if !ok {
		t.Fatal("transform task missing after overlay")
	}
	if transform.Retries == nil || *transform.Retries != 5 {
		t.Errorf("transform retries = %v, want 5", transform.Retries)
	}
	if transform.Env["TZ"] != "UTC" {
		t.Errorf("transform env TZ = %q, want UTC", transform.Env["TZ"])
	}
	if transform.Resources == nil || transform.Resources.Requests == nil || transform.Resources.Requests.CPU != "2" {
		t.Errorf("transform resources = %+v, want cpu 2", transform.Resources)
	}

	// The untouched task keeps its compiled shape (no override leaked onto it).
	extract, _ := taskByID(got, "extract")
	if extract.Retries != nil || extract.Resources != nil || len(extract.Env) != 0 {
		t.Errorf("extract was modified by overlay: %+v", extract)
	}
}

func TestOverlayProjectUnknownTaskIDErrors(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{"typo": {}},
	}
	err := overlayProject(path, cfg)
	if err == nil {
		t.Fatal("expected error for unknown task_id, got nil")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error %q should name the unknown task_id 'typo'", err.Error())
	}
}

func TestOverlayProjectPreservesStaging(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID:   "proj",
		Staging: &domain.StagingConfig{Enabled: true, Size: "5Gi"},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)
	if got.Staging == nil || !got.Staging.Enabled || got.Staging.Size != "5Gi" {
		t.Errorf("staging = %+v, want enabled 5Gi", got.Staging)
	}
}
