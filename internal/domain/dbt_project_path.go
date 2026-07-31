package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrInvalidDbtProject reports a dbt.project path the compiler cannot use.
var ErrInvalidDbtProject = errors.New("invalid dbt.project")

// validateDbtProject rejects a dbt.project path that cannot resolve.
//
// The value is used two ways, and both assume a relative path inside the DAG
// directory: dbtProjectDir resolves it with filepath.Join(dagDir, project), and a
// Pro image build bakes the project at that same relative path inside the image.
//
// filepath.Join does not treat an absolute second element specially, so
// Join("/dags/sales", "/opt/dbt/proj") is "/dags/sales/opt/dbt/proj" — the
// leading slash is swallowed and dbt is pointed at a directory nobody named.
// A path escaping upward resolves outside the DAG directory, and therefore
// outside the Docker build context, so the image cannot contain it either.
//
// Both failures are silent at compile: the path is a string until dbt is invoked
// inside a pod, where it surfaces as "project directory does not exist" with no
// indication that leoflow.yaml was the cause.
func (c *LeoflowConfig) validateDbtProject() error {
	if c.Dbt == nil || c.Dbt.Project == "" {
		return nil
	}
	project := c.Dbt.Project
	if filepath.IsAbs(project) {
		return fmt.Errorf("%w: %q is absolute; it must be relative to the directory holding leoflow.yaml, "+
			"because a Pro image build bakes the project at that relative path inside the image",
			ErrInvalidDbtProject, project)
	}
	// Clean resolves any ".." before the comparison, so "transform/../analytics"
	// is judged on what it actually points at rather than on how it is spelled.
	if clean := filepath.Clean(project); clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q resolves to %q, outside the directory holding leoflow.yaml; "+
			"the Docker build context cannot reach it, so the project would be missing from the image",
			ErrInvalidDbtProject, project, clean)
	}
	return nil
}
