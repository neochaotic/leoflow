// Package executor runs task instances via Kubernetes, Docker, or a subprocess.
package executor

import (
	"context"

	"github.com/neochaotic/leoflow/internal/domain"
)

// PodSecurity holds the task-pod hardening knobs whose defaults are behavioral
// rather than free. Both zero values are the safe choice, so a Request that
// never touches this struct gets a pod that Pod Security Admission's
// `restricted` profile admits.
type PodSecurity struct {
	// RunAsNonRoot refuses to start a task container whose image resolves to
	// UID 0. It completes the `restricted` set, and it is opt-in rather than
	// default for a reason that is about this repo, not about the profile:
	// none of the images Leoflow ships can satisfy it yet. Every
	// examples/*/Dockerfile runs as root, and runtime/Dockerfile declares
	// `USER leoflow` — a name, which the kubelet cannot resolve to a UID, so it
	// rejects the container even though the user is not root. Turning this on
	// by default would mean no example DAG runs on Pro.
	//
	// Flipping the default is tracked as its own change: give the images
	// numeric non-root UIDs, add an fsGroup so the staging PVC stays writable,
	// then make this the default. A secure default the platform's own images
	// cannot meet is not a secure default.
	RunAsNonRoot bool

	// ReadOnlyRootFilesystem mounts the container root read-only. Off by
	// default on purpose: `restricted` does not require it, and it breaks
	// ordinary Python tasks that write to /tmp, the pip cache or a matplotlib
	// config dir. Opt in for a task that is known not to write.
	ReadOnlyRootFilesystem bool
}

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

	// PodSecurity carries the two hardening choices that can change how a task
	// runs. Everything else BuildPod applies is unconditional, because dropping
	// capabilities, blocking privilege escalation and setting a seccomp profile
	// cost a normal task nothing. Zero value is the secure default.
	PodSecurity PodSecurity

	// Source is the dag.py text captured at compile time. The SubprocessExecutor
	// materializes it to a per-TI temp dir so `python -m leoflow_runtime
	// dag:<task>` can importlib it from there — this is how multi-DAG Lite setups
	// avoid the ModuleNotFoundError that hit Lima 2026-06-01 when the agent's
	// global workdir didn't carry the user's dag.py. Empty for Pro (the
	// container image already carries the source); ignored by the K8s executor.
	Source string

	// Agent connection details injected into the worker environment.
	ControlPlaneAddr string
	AgentToken       string

	// StagingClaim, when set, is the name of the per-DAG-run RWX PVC mounted at
	// /staging in the task pod for large intermediate data shared across the run
	// (ADR 0022). Empty means no staging volume. StagingSize/StagingStorageClass
	// are used to provision the claim on first use.
	StagingClaim        string
	StagingSize         string
	StagingStorageClass string
	// StagingAccessMode is the PVC access mode (default ReadWriteMany; single-node
	// dev uses ReadWriteOnce). Empty means ReadWriteMany.
	StagingAccessMode string

	// AgentTLSCAConfigMap, when set, is the name of a ConfigMap holding the CA
	// (key ca.crt) the agent uses to verify the control plane's gRPC TLS cert
	// (issue #58). It is mounted into the task pod and selects TLS for the agent.
	AgentTLSCAConfigMap string

	// TaskSecretName, when set, is a Kubernetes Secret mounted read-only into the
	// task pod at TaskSecretMountPath. It carries a credential a task references by
	// path (e.g. a GCP service-account key via the connection's key_path), keeping
	// the key in the cluster's secret store rather than in Leoflow (ADR 0035).
	TaskSecretName      string
	TaskSecretMountPath string
}

// Executor runs or dispatches a task. For asynchronous executors
// (Kubernetes/Docker/subprocess) the returned error reflects dispatch, and the
// agent reports the final state over gRPC.
type Executor interface {
	Execute(ctx context.Context, req Request) error
}
