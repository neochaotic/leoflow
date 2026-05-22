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

func TestExecutionModeAcceptedForHTTPAPI(t *testing.T) {
	for _, mode := range []ExecutionMode{ExecutionModeInline, ExecutionModePod} {
		spec := validDAGSpec()
		spec.Tasks[1].ExecutionMode = mode
		if err := spec.Validate(); err != nil {
			t.Errorf("http_api with execution_mode %q should be valid: %v", mode, err)
		}
	}
}

func TestExecutionModeRejectedForNonHTTPTasks(t *testing.T) {
	cases := map[string]func(*DAGSpec){
		"python inline": func(d *DAGSpec) { d.Tasks[0].ExecutionMode = ExecutionModeInline },
		"bash inline": func(d *DAGSpec) {
			d.Tasks[0].Type = TaskTypeBash
			d.Tasks[0].Entrypoint = "echo hi"
			d.Tasks[0].ExecutionMode = ExecutionModeInline
		},
		"unknown mode": func(d *DAGSpec) { d.Tasks[1].ExecutionMode = "turbo" },
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

func TestPythonMayDeclarePodMode(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[0].ExecutionMode = ExecutionModePod
	if err := spec.Validate(); err != nil {
		t.Errorf("python with execution_mode pod should be valid: %v", err)
	}
}

func TestEffectiveExecutionModeDefaults(t *testing.T) {
	http := TaskSpec{Type: TaskTypeHTTPAPI}
	if http.EffectiveExecutionMode() != ExecutionModeInline {
		t.Errorf("http_api default = %q, want inline", http.EffectiveExecutionMode())
	}
	pod := TaskSpec{Type: TaskTypeHTTPAPI, ExecutionMode: ExecutionModePod}
	if pod.EffectiveExecutionMode() != ExecutionModePod {
		t.Errorf("explicit mode = %q, want pod", pod.EffectiveExecutionMode())
	}
	py := TaskSpec{Type: TaskTypePython}
	if py.EffectiveExecutionMode() != ExecutionModePod {
		t.Errorf("python default = %q, want pod", py.EffectiveExecutionMode())
	}
}

func TestValidateInlineExecutionRejectsLongInlineHTTP(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[1].ExecutionTimeoutSeconds = ptr(600) // http_api, inline by default
	if err := spec.ValidateInlineExecution(300); err == nil {
		t.Error("inline http_api with timeout above the cap must be rejected")
	}
}

func TestValidateInlineExecutionAllows(t *testing.T) {
	cases := map[string]func(*DAGSpec){
		"inline under cap": func(d *DAGSpec) { d.Tasks[1].ExecutionTimeoutSeconds = ptr(200) },
		"pod over cap": func(d *DAGSpec) {
			d.Tasks[1].ExecutionMode = ExecutionModePod
			d.Tasks[1].ExecutionTimeoutSeconds = ptr(3600)
		},
		"python over cap (pod)": func(d *DAGSpec) { d.Tasks[0].ExecutionTimeoutSeconds = ptr(3600) },
		"no explicit timeout":   func(d *DAGSpec) {},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validDAGSpec()
			mutate(spec)
			if err := spec.ValidateInlineExecution(300); err != nil {
				t.Errorf("ValidateInlineExecution() = %v, want nil for %q", err, name)
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
