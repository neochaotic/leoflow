package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadProjectConfigRejectsDuplicateTaskID guards the YAML↔task binding: a
// duplicated task_id key in the tasks block is a copy-paste hazard, so parsing
// must reject it rather than silently keeping the last entry (ADR 0023).
func TestLoadProjectConfigRejectsDuplicateTaskID(t *testing.T) {
	dir := t.TempDir()
	yaml := strings.Join([]string{
		"dag_id: proj",
		"tasks:",
		"  transform:",
		"    retries: 1",
		"  transform:",
		"    retries: 2",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadProjectConfig(dir)
	if err == nil {
		t.Fatal("expected error for duplicate task_id key, got nil")
	}
	if !strings.Contains(err.Error(), "transform") && !strings.Contains(strings.ToLower(err.Error()), "already") {
		t.Errorf("error %q should flag the duplicate key", err.Error())
	}
}
