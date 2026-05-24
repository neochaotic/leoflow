package domain

// LeoflowConfig is the developer-facing project configuration parsed from
// leoflow.yaml. It mirrors docs/api/leoflow-yaml-schema.json and is consumed
// by `leoflow compile` to build an image and emit a DAGSpec.
type LeoflowConfig struct {
	SchemaVersion  string          `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
	DagID          string          `json:"dag_id" yaml:"dag_id"`
	Description    string          `json:"description,omitempty" yaml:"description,omitempty"`
	Owner          string          `json:"owner,omitempty" yaml:"owner,omitempty"`
	Tags           []string        `json:"tags,omitempty" yaml:"tags,omitempty"`
	PythonVersion  string          `json:"python_version,omitempty" yaml:"python_version,omitempty"`
	BaseImage      string          `json:"base_image,omitempty" yaml:"base_image,omitempty"`
	Dependencies   []string        `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	SystemPackages []string        `json:"system_packages,omitempty" yaml:"system_packages,omitempty"`
	DagSource      string          `json:"dag_source,omitempty" yaml:"dag_source,omitempty"`
	IncludePaths   []string        `json:"include_paths,omitempty" yaml:"include_paths,omitempty"`
	ExcludePaths   []string        `json:"exclude_paths,omitempty" yaml:"exclude_paths,omitempty"`
	Build          *BuildConfig    `json:"build,omitempty" yaml:"build,omitempty"`
	Registry       *RegistryConfig `json:"registry,omitempty" yaml:"registry,omitempty"`
	Defaults       *ConfigDefaults `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	// Staging requests the opt-in per-DAG-run shared volume (ADR 0022). It is a
	// Leoflow deployment concern (not an Airflow DAG attribute), so it lives in
	// leoflow.yaml and the compiler overlays it onto the produced dag.json.
	Staging *StagingConfig `json:"staging,omitempty" yaml:"staging,omitempty"`
}

// BuildConfig controls how the container image is built from the project.
type BuildConfig struct {
	Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
	Context    string            `json:"context,omitempty" yaml:"context,omitempty"`
	Platforms  []string          `json:"platforms,omitempty" yaml:"platforms,omitempty"`
	Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// RegistryConfig describes where the built image is pushed and how it is tagged.
type RegistryConfig struct {
	URL         string `json:"url,omitempty" yaml:"url,omitempty"`
	AuthMethod  string `json:"auth_method,omitempty" yaml:"auth_method,omitempty"`
	ImageName   string `json:"image_name,omitempty" yaml:"image_name,omitempty"`
	TagStrategy string `json:"tag_strategy,omitempty" yaml:"tag_strategy,omitempty"`
}

// ConfigDefaults holds task defaults applied to every task generated from the
// project at compile time.
type ConfigDefaults struct {
	Retries                 int               `json:"retries,omitempty" yaml:"retries,omitempty"`
	RetryDelaySeconds       int               `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds int               `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
	Resources               *DefaultResources `json:"resources,omitempty" yaml:"resources,omitempty"`
}

// DefaultResources expresses default CPU and memory for generated tasks.
type DefaultResources struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// Validate checks the LeoflowConfig against the canonical leoflow.yaml schema
// and returns a joined error describing every violation, or nil when valid.
func (c *LeoflowConfig) Validate() error {
	s, err := schemas()
	if err != nil {
		return err
	}
	return validateAgainst(s.leoflow, c)
}
