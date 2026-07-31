// Package domain defines the core Leoflow types (DAG, Task, project config)
// and validates them against the canonical JSON Schemas in docs/api.
package domain

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// DefaultInlineMaxDurationSeconds is the fallback cap on inline http_api task
// duration when the server does not configure one. See ADR 0002.
const DefaultInlineMaxDurationSeconds = 300

// TaskType enumerates the kinds of work a task can perform.
type TaskType string

// Supported task types. See docs/api/dag-schema.json.
const (
	// TaskTypePython runs a Python callable identified by an entrypoint.
	TaskTypePython TaskType = "python"
	// TaskTypeBash runs a shell command supplied as the entrypoint.
	TaskTypeBash TaskType = "bash"
	// TaskTypeHTTPAPI performs an outbound HTTP request from the control plane.
	TaskTypeHTTPAPI TaskType = "http_api"
	// TaskTypeAirflowOperator runs a captured Airflow provider operator/sensor in
	// the task pod via the generic executor (ADR 0040): the runtime instantiates
	// OperatorClass with OperatorArgs and calls execute(). The provider is
	// installed in the image via connectors:/dependencies:.
	TaskTypeAirflowOperator TaskType = "airflow_operator"
	// TaskTypeDbtGroup is a transient placeholder for a dbt project embedded in a
	// DAG (ADR 0043). The compiler expands it into one task per dbt node and the
	// type never appears in a finished dag.json.
	TaskTypeDbtGroup TaskType = "dbt_group"
)

// ExecutionMode selects how a task runs. It is only meaningful for http_api
// tasks; python and bash tasks always run in a pod.
type ExecutionMode string

// Supported execution modes. See docs/api/dag-schema.json and ADR 0002.
const (
	// ExecutionModeInline runs an http_api task as a goroutine in the control
	// plane, capped at the server's inline duration limit.
	ExecutionModeInline ExecutionMode = "inline"
	// ExecutionModePod runs a task inside a worker pod via the agent.
	ExecutionModePod ExecutionMode = "pod"
)

// TriggerRule decides whether a task runs based on its upstreams' states.
type TriggerRule string

// Supported trigger rules for the MVP. See docs/api/dag-schema.json.
const (
	// TriggerRuleAllSuccess runs when every upstream succeeded (default).
	TriggerRuleAllSuccess TriggerRule = "all_success"
	// TriggerRuleAllFailed runs when every upstream failed.
	TriggerRuleAllFailed TriggerRule = "all_failed"
	// TriggerRuleAllDone runs once every upstream finished, regardless of state.
	TriggerRuleAllDone TriggerRule = "all_done"
	// TriggerRuleOneSuccess runs as soon as one upstream succeeds.
	TriggerRuleOneSuccess TriggerRule = "one_success"
	// TriggerRuleOneFailed runs as soon as one upstream fails.
	TriggerRuleOneFailed TriggerRule = "one_failed"
)

// DAGSpec is the canonical serialized representation of a DAG consumed by the
// control plane. It mirrors docs/api/dag-schema.json.
type DAGSpec struct {
	SchemaVersion string       `json:"schema_version"`
	DagID         string       `json:"dag_id"`
	DagVersion    string       `json:"dag_version"`
	Image         string       `json:"image"`
	Description   string       `json:"description,omitempty"`
	Owner         string       `json:"owner,omitempty"`
	Tags          []string     `json:"tags,omitempty"`
	Schedule      *string      `json:"schedule,omitempty"`
	ScheduleTZ    string       `json:"schedule_timezone,omitempty"`
	StartDate     string       `json:"start_date,omitempty"`
	EndDate       *string      `json:"end_date,omitempty"`
	MaxActiveRuns int          `json:"max_active_runs,omitempty"`
	Catchup       bool         `json:"catchup,omitempty"`
	DefaultArgs   *DefaultArgs `json:"default_args,omitempty"`
	// Staging, when enabled, requests an ephemeral RWX volume shared by the run's
	// tasks at /staging (ADR 0022). nil/disabled means no staging volume.
	Staging *StagingConfig `json:"staging,omitempty"`
	// Alerts declares native on-failure alerting (#424), overlaid from leoflow.yaml
	// at compile time so the scheduler fires it from the artifact without re-reading
	// the project config. nil means no alerting.
	Alerts *AlertsConfig `json:"alerts,omitempty"`
	Tasks  []TaskSpec    `json:"tasks"`
	// Source is the original dag.py text, captured at compile time so the UI's
	// Code tab can show the Python a human wrote (not the compiled spec). It is
	// part of the artifact: changing it produces a new version.
	Source string `json:"source,omitempty"`
}

// StagingConfig is the opt-in per-DAG-run shared staging volume (ADR 0022). Size
// is a Kubernetes quantity (e.g. "5Gi"); StorageClass empty uses the cluster
// default RWX class.
type StagingConfig struct {
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	Size         string `json:"size,omitempty" yaml:"size,omitempty"`
	StorageClass string `json:"storage_class,omitempty" yaml:"storage_class,omitempty"`
}

// DefaultArgs holds retry and timeout defaults applied to every task in a DAG.
type DefaultArgs struct {
	Retries                 int `json:"retries,omitempty"`
	RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty"`
}

// TaskSpec describes a single unit of work within a DAG.
type TaskSpec struct {
	TaskID                  string              `json:"task_id"`
	Type                    TaskType            `json:"type"`
	DependsOn               []string            `json:"depends_on,omitempty"`
	TriggerRule             TriggerRule         `json:"trigger_rule,omitempty"`
	Retries                 *int                `json:"retries,omitempty"`
	RetryDelaySeconds       *int                `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds *int                `json:"execution_timeout_seconds,omitempty"`
	ExecutionMode           ExecutionMode       `json:"execution_mode,omitempty"`
	Entrypoint              string              `json:"entrypoint,omitempty"`
	HTTPRequest             *HTTPRequest        `json:"http_request,omitempty"`
	Env                     map[string]string   `json:"env,omitempty"`
	Secrets                 []Secret            `json:"secrets,omitempty"`
	Resources               *Resources          `json:"resources,omitempty"`
	Execution               *Execution          `json:"execution,omitempty"`
	XComInput               map[string][]string `json:"xcom_input,omitempty"`
	XComSchema              map[string]any      `json:"xcom_schema,omitempty"`
	// CallArgs carries TaskFlow literal call arguments captured at compile time
	// (#115). The agent serializes the whole map as a single env var
	// LEOFLOW_CALL_ARGS_JSON; the runtime decodes and delivers each value to
	// the user function. XCom upstreams take precedence at runtime over a
	// same-name literal (the deterministic merge owned by leoflow_runtime).
	// Named call_args (not params) to leave the term free for Airflow's
	// DAG-run params semantic (#148).
	CallArgs map[string]any `json:"call_args,omitempty"`
	// OperatorClass is the dotted Airflow operator/sensor class for an
	// airflow_operator task (ADR 0040), e.g.
	// "airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator".
	OperatorClass string `json:"operator_class,omitempty"`
	// OperatorArgs are the operator's constructor kwargs captured at compile time.
	// The agent serializes them as the env var LEOFLOW_OPERATOR_ARGS; the runtime
	// instantiates the operator with them.
	OperatorArgs map[string]any `json:"operator_args,omitempty"`
	// OnFailureCallback marks that the task declares an Airflow on_failure_callback
	// (#424). The callable itself is not carried (it can't be serialized); the
	// runtime re-imports dag.py and runs it in the task process on failure. The
	// flag lets the agent/UI know a callback will run without importing user code.
	OnFailureCallback bool `json:"on_failure_callback,omitempty"`
}

// HTTPRequest is the request executed directly by the control plane for
// http_api tasks.
type HTTPRequest struct {
	Method             string            `json:"method"`
	URL                string            `json:"url"`
	Headers            map[string]string `json:"headers,omitempty"`
	Body               any               `json:"body,omitempty"`
	TimeoutSeconds     int               `json:"timeout_seconds,omitempty"`
	SuccessStatusCodes []int             `json:"success_status_codes,omitempty"`
}

// Secret references a credential injected into the worker at run time.
type Secret struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Reference string `json:"reference,omitempty"`
}

// Resources holds Kubernetes-style resource requests and limits for a task.
type Resources struct {
	Requests *ResourceQuantity `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits   *ResourceQuantity `json:"limits,omitempty" yaml:"limits,omitempty"`
}

// ResourceQuantity expresses CPU and memory in Kubernetes notation.
type ResourceQuantity struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// Execution carries executor-specific placement hints for a task.
type Execution struct {
	NodeSelector    map[string]string `json:"node_selector,omitempty" yaml:"node_selector,omitempty"`
	Tolerations     []map[string]any  `json:"tolerations,omitempty" yaml:"tolerations,omitempty"`
	ServiceAccount  string            `json:"service_account,omitempty" yaml:"service_account,omitempty"`
	ImagePullPolicy string            `json:"image_pull_policy,omitempty" yaml:"image_pull_policy,omitempty"`
}

// EffectiveExecutionMode returns the task's execution mode, applying the
// defaults: http_api tasks default to inline, every other type runs in a pod.
func (t TaskSpec) EffectiveExecutionMode() ExecutionMode {
	if t.ExecutionMode != "" {
		return t.ExecutionMode
	}
	if t.Type == TaskTypeHTTPAPI {
		return ExecutionModeInline
	}
	return ExecutionModePod
}

// ValidateInlineExecution rejects inline http_api tasks whose
// execution_timeout_seconds exceeds the server's inline duration cap. Such a
// task must declare execution_mode: pod. maxInlineSeconds is the server limit.
func (d *DAGSpec) ValidateInlineExecution(maxInlineSeconds int) error {
	for _, t := range d.Tasks {
		if t.Type != TaskTypeHTTPAPI || t.EffectiveExecutionMode() != ExecutionModeInline {
			continue
		}
		if t.ExecutionTimeoutSeconds != nil && *t.ExecutionTimeoutSeconds > maxInlineSeconds {
			return fmt.Errorf(
				"task %q declares execution_timeout_seconds=%d but inline http_api tasks are capped at %d seconds on this server; set execution_mode: pod to use a worker pod, which has no such cap",
				t.TaskID, *t.ExecutionTimeoutSeconds, maxInlineSeconds)
		}
	}
	return nil
}

// Validate checks the DAGSpec against the canonical dag.json schema and
// returns a joined error describing every schema violation, or nil when valid.
func (d *DAGSpec) Validate() error {
	s, err := schemas()
	if err != nil {
		return err
	}
	if err := validateAgainst(s.dag, d); err != nil {
		return err
	}
	if err := d.validateGraph(); err != nil {
		return err
	}
	return d.validateResourceQuantities()
}

// validateResourceQuantities rejects a CPU or memory value Kubernetes cannot
// parse. The schema types these as strings — a JSON Schema cannot express the
// quantity grammar — so without this they pass validation and are dropped at pod
// build (executor.quantities keeps only what ParseQuantity accepts).
//
// Dropping is the dangerous outcome, not the parse failure: a task declaring
// `memory: 2GB` got a pod with NO memory limit, which is the opposite of what it
// asked for, on a shared node, silently. And `2GB` is the plausible typo — it is
// how memory is written everywhere except Kubernetes, which wants `2Gi` or `2G`.
//
// Checked here so `leoflow compile` fails while the author is still looking at
// it, rather than at registration or, worse, at dispatch.
func (d *DAGSpec) validateResourceQuantities() error {
	for _, t := range d.Tasks {
		if t.Resources == nil {
			continue
		}
		for _, q := range []struct {
			field string
			val   *ResourceQuantity
		}{
			{"requests", t.Resources.Requests},
			{"limits", t.Resources.Limits},
		} {
			if q.val == nil {
				continue
			}
			for _, f := range []struct{ name, value string }{
				{"cpu", q.val.CPU},
				{"memory", q.val.Memory},
			} {
				if f.value == "" {
					continue
				}
				if _, err := resource.ParseQuantity(f.value); err != nil {
					return fmt.Errorf(
						"task %q declares resources.%s.%s = %q, which is not a valid Kubernetes quantity: %w. "+
							"Use Kubernetes notation — CPU as cores or millicores (\"2\", \"500m\"), memory with a binary or decimal suffix (\"2Gi\", \"2G\", \"512Mi\"). "+
							"Note \"2GB\" is not valid; Kubernetes spells it \"2Gi\" (1024-based) or \"2G\" (1000-based)",
						t.TaskID, q.field, f.name, f.value, err)
				}
			}
		}
	}
	return nil
}
