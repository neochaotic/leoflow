// Package dispatch launches pod-path task instances: it resolves a task's
// execution context, mints the agent's identity token, and routes the request
// to the executor. It implements scheduler.Dispatcher.
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/executor"
)

// Resolved is the execution context the dispatcher needs to launch a task.
type Resolved struct {
	TaskInstanceID  string
	TenantID        string
	Image           string
	ImagePullPolicy string
	TryNumber       int
	// Staging carries the DAG's opt-in staging-volume config (ADR 0022); nil or
	// disabled means no per-run volume.
	Staging *domain.StagingConfig
}

// Resolver loads a task instance's execution context from storage.
type Resolver interface {
	ResolveTask(ctx context.Context, runID, taskID string) (Resolved, error)
}

// TokenIssuer mints a per-task-instance agent token.
type TokenIssuer interface {
	IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}

// Dispatcher builds executor requests for queued pod-path tasks and runs them.
type Dispatcher struct {
	exec           executor.Executor
	resolver       Resolver
	issuer         TokenIssuer
	controlAddr    string
	tokenTTL       time.Duration
	tlsCAConfigMap string
}

// NewDispatcher builds a Dispatcher that launches tasks via exec, resolves their
// context with resolver, mints tokens with issuer (valid for tokenTTL), and
// tells the agent to reach the control plane at controlAddr.
func NewDispatcher(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration) *Dispatcher {
	return &Dispatcher{exec: exec, resolver: resolver, issuer: issuer, controlAddr: controlAddr, tokenTTL: tokenTTL}
}

// SetAgentTLSCAConfigMap configures the CA ConfigMap mounted into task pods so
// agents verify the control plane's gRPC TLS cert (issue #58). Empty = the agent
// stays on the insecure channel (dev).
func (d *Dispatcher) SetAgentTLSCAConfigMap(name string) { d.tlsCAConfigMap = name }

// Dispatch resolves the task, mints its agent token, and executes it.
func (d *Dispatcher) Dispatch(ctx context.Context, runID, dagID string, task domain.TaskSpec) error {
	r, err := d.resolver.ResolveTask(ctx, runID, task.TaskID)
	if err != nil {
		return fmt.Errorf("resolving task %s: %w", task.TaskID, err)
	}
	token, err := d.issuer.IssueAgentToken(auth.AgentIdentity{
		TaskInstanceID: r.TaskInstanceID,
		TenantID:       r.TenantID,
		DagID:          dagID,
		RunID:          runID,
		TaskID:         task.TaskID,
		TryNumber:      r.TryNumber,
	}, d.tokenTTL)
	if err != nil {
		return fmt.Errorf("issuing agent token for %s: %w", task.TaskID, err)
	}

	req := executor.Request{
		TaskInstanceID:   r.TaskInstanceID,
		TenantID:         r.TenantID,
		DagID:            dagID,
		RunID:            runID,
		TaskID:           task.TaskID,
		TryNumber:        r.TryNumber,
		Image:            r.Image,
		ImagePullPolicy:  r.ImagePullPolicy,
		Operator:         string(task.Type),
		Entrypoint:       task.Entrypoint,
		Env:              task.Env,
		HTTPRequest:      task.HTTPRequest,
		ControlPlaneAddr: d.controlAddr,
		AgentToken:       token,
	}
	if task.ExecutionTimeoutSeconds != nil {
		req.TimeoutSeconds = *task.ExecutionTimeoutSeconds
	}
	if task.Resources != nil {
		req.Resources = *task.Resources
	}
	if task.Execution != nil {
		req.Execution = *task.Execution
	}
	if r.Staging != nil && r.Staging.Enabled {
		// All of the run's tasks share one PVC, named deterministically so a
		// clear+re-run re-attaches it (ADR 0022). The executor provisions it.
		req.StagingClaim = executor.StagingClaimName(dagID, runID)
		req.StagingSize = r.Staging.Size
		req.StagingStorageClass = r.Staging.StorageClass
	}
	req.AgentTLSCAConfigMap = d.tlsCAConfigMap
	return d.exec.Execute(ctx, req)
}
