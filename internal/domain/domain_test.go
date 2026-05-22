package domain

import "testing"

func ptr[T any](v T) *T { return &v }

func validDAGSpec() *DAGSpec {
	return &DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "etl_sales",
		DagVersion:    "v1.2.3",
		Image:         "myrepo/etl-sales:v1.2.3",
		Schedule:      ptr("0 5 * * *"),
		Tasks: []TaskSpec{
			{
				TaskID:      "extract",
				Type:        TaskTypePython,
				Entrypoint:  "tasks.extract:run",
				TriggerRule: TriggerRuleAllSuccess,
			},
			{
				TaskID:      "notify",
				Type:        TaskTypeHTTPAPI,
				DependsOn:   []string{"extract"},
				HTTPRequest: &HTTPRequest{Method: "POST", URL: "https://example.com/hook"},
			},
		},
	}
}

func validLeoflowConfig() *LeoflowConfig {
	return &LeoflowConfig{
		SchemaVersion: "1.0",
		DagID:         "etl_sales",
		PythonVersion: "3.11",
		Dependencies:  []string{"pandas==2.1.0"},
	}
}

func TestDAGSpecValidateAcceptsValidSpec(t *testing.T) {
	if err := validDAGSpec().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestDAGSpecValidateRejectsInvalidSpecs(t *testing.T) {
	cases := map[string]func(*DAGSpec){
		"missing dag_id":       func(d *DAGSpec) { d.DagID = "" },
		"no tasks":             func(d *DAGSpec) { d.Tasks = nil },
		"bad schema_version":   func(d *DAGSpec) { d.SchemaVersion = "2.0" },
		"unknown task type":    func(d *DAGSpec) { d.Tasks[0].Type = "ruby" },
		"bad trigger rule":     func(d *DAGSpec) { d.Tasks[0].TriggerRule = "sometimes" },
		"python without entry": func(d *DAGSpec) { d.Tasks[0].Entrypoint = "" },
		"http without request": func(d *DAGSpec) { d.Tasks[1].HTTPRequest = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validDAGSpec()
			mutate(spec)
			if err := spec.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %q", name)
			}
		})
	}
}

func TestLeoflowConfigValidateAcceptsValidConfig(t *testing.T) {
	if err := validLeoflowConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestLeoflowConfigValidateRejectsInvalidConfigs(t *testing.T) {
	cases := map[string]func(*LeoflowConfig){
		"missing dag_id":     func(c *LeoflowConfig) { c.DagID = "" },
		"bad python version": func(c *LeoflowConfig) { c.PythonVersion = "2.7" },
		"bad dag_id pattern": func(c *LeoflowConfig) { c.DagID = "has spaces" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validLeoflowConfig()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %q", name)
			}
		})
	}
}
