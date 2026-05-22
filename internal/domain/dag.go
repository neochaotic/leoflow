// Package domain defines the core Leoflow types (DAG, Task, project config)
// and validates them against the canonical JSON Schemas in docs/api.
package domain

import "fmt"

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
	Tasks         []TaskSpec   `json:"tasks"`
}

// DefaultArgs holds retry and timeout defaults applied to every task in a DAG.
type DefaultArgs struct {
	Retries                 int `json:"retries,omitempty"`
	RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty"`
}

// TaskSpec describes a single unit of work within a DAG.
type TaskSpec struct {
	TaskID                  string            `json:"task_id"`
	Type                    TaskType          `json:"type"`
	DependsOn               []string          `json:"depends_on,omitempty"`
	TriggerRule             TriggerRule       `json:"trigger_rule,omitempty"`
	Retries                 *int              `json:"retries,omitempty"`
	RetryDelaySeconds       *int              `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds *int              `json:"execution_timeout_seconds,omitempty"`
	ExecutionMode           ExecutionMode     `json:"execution_mode,omitempty"`
	Entrypoint              string            `json:"entrypoint,omitempty"`
	HTTPRequest             *HTTPRequest      `json:"http_request,omitempty"`
	Env                     map[string]string `json:"env,omitempty"`
	Secrets                 []Secret          `json:"secrets,omitempty"`
	Resources               *Resources        `json:"resources,omitempty"`
	Execution               *Execution        `json:"execution,omitempty"`
	XComInput               map[string]string `json:"xcom_input,omitempty"`
	XComSchema              map[string]any    `json:"xcom_schema,omitempty"`
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
	Requests *ResourceQuantity `json:"requests,omitempty"`
	Limits   *ResourceQuantity `json:"limits,omitempty"`
}

// ResourceQuantity expresses CPU and memory in Kubernetes notation.
type ResourceQuantity struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// Execution carries executor-specific placement hints for a task.
type Execution struct {
	NodeSelector    map[string]string `json:"node_selector,omitempty"`
	Tolerations     []map[string]any  `json:"tolerations,omitempty"`
	ServiceAccount  string            `json:"service_account,omitempty"`
	ImagePullPolicy string            `json:"image_pull_policy,omitempty"`
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
	return validateAgainst(s.dag, d)
}
