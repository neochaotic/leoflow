// Package executor runs task instances via Kubernetes, Docker, a subprocess, or
// inline HTTP, selected by the Router (ADR 0002).
package executor

import (
	"context"

	"github.com/neochaotic/leoflow/internal/domain"
)

// Request bundles everything an executor needs to run a single task instance.
type Request struct {
	TaskInstanceID string
	TenantID       string
	DagID          string
	RunID          string
	TaskID         string
	TryNumber      int

	Image           string
	ImagePullPolicy string
	Operator        string
	Entrypoint      string
	Env             map[string]string
	Resources       domain.Resources
	Execution       domain.Execution
	TimeoutSeconds  int

	// HTTPRequest is set for http_api tasks (run by InlineHTTPExecutor).
	HTTPRequest *domain.HTTPRequest

	// Agent connection details injected into the worker environment.
	ControlPlaneAddr string
	AgentToken       string

	// StagingClaim, when set, is the name of the per-DAG-run RWX PVC mounted at
	// /staging in the task pod for large intermediate data shared across the run
	// (ADR 0022). Empty means no staging volume.
	StagingClaim string
}

// Executor runs or dispatches a task. For synchronous executors (inline HTTP)
// the returned error reflects the task outcome; for asynchronous executors
// (Kubernetes/Docker/subprocess) it reflects dispatch, and the agent reports
// the final state over gRPC.
type Executor interface {
	Execute(ctx context.Context, req Request) error
}

// Router selects the executor for a task: http_api tasks run inline in the
// control plane; everything else uses the configured standard executor.
type Router struct {
	standard Executor
	inline   Executor
}

// NewRouter builds a Router over the standard (k8s/docker/subprocess) and inline
// HTTP executors.
func NewRouter(standard, inline Executor) *Router {
	return &Router{standard: standard, inline: inline}
}

// ExecutorFor returns the executor responsible for a task of the given operator.
func (r *Router) ExecutorFor(operator string) Executor {
	if operator == "http_api" && r.inline != nil {
		return r.inline
	}
	return r.standard
}
