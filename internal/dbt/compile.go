package dbt

import (
	"fmt"

	"github.com/neochaotic/leoflow/internal/domain"
)

// Meta carries the DAG metadata a dbt manifest does not provide: identity,
// version, image, ownership, schedule, and the granularity strategy. These come
// from the Leoflow project config, not from dbt.
type Meta struct {
	DagID       string
	DagVersion  string
	Image       string
	Owner       string
	Description string
	Tags        []string
	Schedule    string
	Granularity Granularity
	// Connection and Profile, when set, wrap each task's dbt command with the
	// runtime step that writes profiles.yml from the managed connection (ADR 0043).
	Connection string
	Profile    string
	// Schema overrides the dbt target schema in the generated profile.
	Schema string
	// ProjectDir scopes the dbt commands with --project-dir for a subdir project.
	ProjectDir string
	// Local marks a Lite/host build: with no Connection, each task is prefixed with
	// the step that writes a default duckdb profiles.yml (the zero-config local
	// warehouse). Ignored on the Pro/image path. See Options.Local.
	Local bool
}

// Compile renders a dbt manifest.json into tasks and assembles a complete
// dag.json DAGSpec around them. DagID is required; an empty Schedule yields an
// unscheduled DAG (the field is omitted, not set to "").
func Compile(manifestJSON []byte, meta Meta) (domain.DAGSpec, error) {
	if meta.DagID == "" {
		return domain.DAGSpec{}, fmt.Errorf("dbt compile: dag_id is required")
	}
	tasks, err := Render(manifestJSON, Options{
		Granularity: meta.Granularity,
		Connection:  meta.Connection,
		Profile:     meta.Profile,
		Schema:      meta.Schema,
		ProjectDir:  meta.ProjectDir,
		Local:       meta.Local,
	})
	if err != nil {
		return domain.DAGSpec{}, err
	}
	spec := domain.DAGSpec{
		SchemaVersion: "1.0",
		DagID:         meta.DagID,
		DagVersion:    meta.DagVersion,
		Image:         meta.Image,
		Owner:         meta.Owner,
		Description:   meta.Description,
		Tags:          meta.Tags,
		Tasks:         tasks,
	}
	if meta.Schedule != "" {
		schedule := meta.Schedule
		spec.Schedule = &schedule
	}
	return spec, nil
}
