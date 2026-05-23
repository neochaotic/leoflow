package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// classRefDTO is the Airflow 3.2.1 ClassReference.
type classRefDTO struct {
	ModulePath *string `json:"module_path"`
	ClassName  string  `json:"class_name"`
}

// taskResponseDTO is the Airflow 3.2.1 TaskResponse. Leoflow models a small
// subset of operator attributes, so the rest are sensible defaults / null — the
// Tasks tab renders the task_id, operator, trigger rule, retries, and downstream
// links from real data.
type taskResponseDTO struct {
	TaskID                  string          `json:"task_id"`
	TaskDisplayName         string          `json:"task_display_name"`
	Owner                   *string         `json:"owner"`
	OperatorName            string          `json:"operator_name"`
	ClassRef                classRefDTO     `json:"class_ref"`
	TriggerRule             string          `json:"trigger_rule"`
	DependsOnPast           bool            `json:"depends_on_past"`
	WaitForDownstream       bool            `json:"wait_for_downstream"`
	Retries                 int             `json:"retries"`
	RetryDelay              *string         `json:"retry_delay"`
	RetryExponentialBackoff bool            `json:"retry_exponential_backoff"`
	DownstreamTaskIDs       []string        `json:"downstream_task_ids"`
	IsMapped                bool            `json:"is_mapped"`
	StartDate               *string         `json:"start_date"`
	EndDate                 *string         `json:"end_date"`
	ExecutionTimeout        *string         `json:"execution_timeout"`
	DocMd                   *string         `json:"doc_md"`
	Pool                    string          `json:"pool"`
	PoolSlots               int             `json:"pool_slots"`
	Queue                   *string         `json:"queue"`
	PriorityWeight          int             `json:"priority_weight"`
	WeightRule              string          `json:"weight_rule"`
	UIColor                 string          `json:"ui_color"`
	UIFgColor               string          `json:"ui_fgcolor"`
	TemplateFields          []string        `json:"template_fields"`
	ExtraLinks              []string        `json:"extra_links"`
	Params                  json.RawMessage `json:"params"`
}

type taskCollectionDTO struct {
	Tasks        []taskResponseDTO `json:"tasks"`
	TotalEntries int               `json:"total_entries"`
}

// operatorName maps a Leoflow task type to an Airflow-style operator name the UI
// displays.
func operatorName(t domain.TaskType) string {
	switch t {
	case domain.TaskTypePython:
		return "PythonOperator"
	case domain.TaskTypeBash:
		return "BashOperator"
	case domain.TaskTypeHTTPAPI:
		return "HttpOperator"
	default:
		return string(t)
	}
}

// downstreamIDs returns the ids of tasks that declare taskID as an upstream.
func downstreamIDs(tasks []domain.TaskSpec, taskID string) []string {
	out := []string{}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if dep == taskID {
				out = append(out, t.TaskID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func toTaskResponse(spec domain.DAGSpec, t domain.TaskSpec) taskResponseDTO {
	tr := strSafe(string(t.TriggerRule), "all_success")
	retries := 0
	if t.Retries != nil {
		retries = *t.Retries
	}
	return taskResponseDTO{
		TaskID:            t.TaskID,
		TaskDisplayName:   t.TaskID,
		Owner:             strPtrOrNil(spec.Owner),
		OperatorName:      operatorName(t.Type),
		ClassRef:          classRefDTO{ClassName: operatorName(t.Type)},
		TriggerRule:       tr,
		Retries:           retries,
		DownstreamTaskIDs: downstreamIDs(spec.Tasks, t.TaskID),
		Pool:              "default_pool",
		PoolSlots:         1,
		PriorityWeight:    1,
		WeightRule:        "downstream",
		UIColor:           "#fff",
		UIFgColor:         "#000",
		TemplateFields:    []string{},
		ExtraLinks:        []string{},
		Params:            json.RawMessage("{}"),
	}
}

// strSafe returns s, or the fallback when s is empty.
func strSafe(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// tasksHandler implements GET /api/v2/dags/{dag_id}/tasks.
func tasksHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		sorted := topoSortTasks(spec.Tasks)
		out := taskCollectionDTO{Tasks: make([]taskResponseDTO, 0, len(sorted)), TotalEntries: len(sorted)}
		for _, t := range sorted {
			out.Tasks = append(out.Tasks, toTaskResponse(spec, t))
		}
		c.JSON(http.StatusOK, out)
	}
}

// taskDetailHandler implements GET /api/v2/dags/{dag_id}/tasks/{task_id}.
func taskDetailHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		taskID := c.Param("task_id")
		for _, t := range spec.Tasks {
			if t.TaskID == taskID {
				c.JSON(http.StatusOK, toTaskResponse(spec, t))
				return
			}
		}
		AbortProblem(c, http.StatusNotFound, "not found", "task not found")
	}
}

// dagSourceHandler implements GET /api/v2/dagSources/{dag_id} — the Code view.
// It returns the original dag.py Python (captured at compile time), matching
// Airflow. Versions compiled before source capture fall back to the compiled
// spec JSON so the tab still renders.
func dagSourceHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		content := spec.Source
		if content == "" {
			marshaled, merr := json.MarshalIndent(spec, "", "  ")
			if merr != nil {
				AbortProblem(c, http.StatusInternalServerError, "internal error", "encoding spec")
				return
			}
			content = string(marshaled)
		}
		c.JSON(http.StatusOK, gin.H{
			"content":          content,
			"dag_id":           dagID,
			"version_number":   1,
			"dag_display_name": dagID,
		})
	}
}

// registerUITasks mounts the task + source endpoints when a spec reader is set.
func registerUITasks(r gin.IRouter, specs DagSpecReader) {
	if specs == nil {
		return
	}
	r.GET("/api/v2/dags/:dag_id/tasks", RequirePermission("read", "task"), tasksHandler(specs))
	r.GET("/api/v2/dags/:dag_id/tasks/:task_id", RequirePermission("read", "task"), taskDetailHandler(specs))
	r.GET("/api/v2/dagSources/:dag_id", RequirePermission("read", "dag"), dagSourceHandler(specs))
}
