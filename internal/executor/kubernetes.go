package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/neochaotic/leoflow/internal/domain"
)

// KubernetesExecutor runs each task as an ephemeral pod (ADR 0002).
type KubernetesExecutor struct {
	clientset kubernetes.Interface
	namespace string
	staging   StagingStore
}

// SetStagingStore wires the metadatabase-backed staging-volume lifecycle store
// (ADR 0022). With no store set, provisioning is not recorded and GC is a no-op.
func (e *KubernetesExecutor) SetStagingStore(s StagingStore) { e.staging = s }

// NewKubernetesExecutor builds an executor creating pods in the given namespace.
func NewKubernetesExecutor(clientset kubernetes.Interface, namespace string) *KubernetesExecutor {
	if namespace == "" {
		namespace = "default"
	}
	return &KubernetesExecutor{clientset: clientset, namespace: namespace}
}

// Execute creates the task pod. The agent inside the pod reports state over gRPC.
func (e *KubernetesExecutor) Execute(ctx context.Context, req Request) error {
	// Provision the run's shared staging PVC on first use (idempotent), before the
	// pod that mounts it (ADR 0022).
	if req.StagingClaim != "" {
		if err := e.ensureStagingClaim(ctx, req); err != nil {
			return fmt.Errorf("provisioning staging volume for task %s: %w", req.TaskID, err)
		}
	}
	pod := BuildPod(req)
	if _, err := e.clientset.CoreV1().Pods(e.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating pod for task %s: %w", req.TaskID, err)
	}
	return nil
}

// terminationMessagePath is where the task container surfaces its termination
// message. The agent writes its durable outcome record here and the reconciler
// reads it from pod status (ADR 0052); it is the Kubernetes default path, pinned
// explicitly so the contract does not rely on an implicit default.
const terminationMessagePath = "/dev/termination-log"

// BuildPod constructs the pod spec for a task instance. It is pure (modulo the
// random name suffix) and unit-tested independently of any cluster.
func BuildPod(req Request) *corev1.Pod {
	pullPolicy := corev1.PullIfNotPresent
	if req.ImagePullPolicy != "" {
		pullPolicy = corev1.PullPolicy(req.ImagePullPolicy)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName(req),
			Labels: map[string]string{
				"leoflow.io/dag-id":     sanitizeLabel(req.DagID),
				"leoflow.io/task-id":    sanitizeLabel(req.TaskID),
				"leoflow.io/run-id":     sanitizeLabel(req.RunID),
				"leoflow.io/try-number": strconv.Itoa(req.TryNumber),
				"leoflow.io/tenant-id":  sanitizeLabel(req.TenantID),
			},
			Annotations: map[string]string{"leoflow.io/task-instance-id": req.TaskInstanceID},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeSelector:  req.Execution.NodeSelector,
			Tolerations:   buildTolerations(req.Execution.Tolerations),
			// Placement and scheduling passthrough for a shared cluster (ADR 0054):
			// pin/spread/prioritize task pods and run accelerator (DRA) DAGs. Each
			// is applied verbatim from the declaration, mirroring tolerations; a
			// value left unset stays the zero value so a DAG that declares none is
			// byte-identical to today.
			PriorityClassName:             req.Execution.PriorityClassName,
			TerminationGracePeriodSeconds: req.Execution.TerminationGracePeriodSeconds,
			RuntimeClassName:              req.Execution.RuntimeClassName,
			TopologySpreadConstraints:     decodeStructuredSlice[corev1.TopologySpreadConstraint](req.Execution.TopologySpreadConstraints),
			Affinity:                      buildAffinity(req.Execution.Affinity),
			ResourceClaims:                decodeStructuredSlice[corev1.PodResourceClaim](req.Execution.ResourceClaims),
			// The task pod authenticates to the control plane with its own
			// per-task token over gRPC and never calls the Kubernetes API, so a
			// mounted ServiceAccount token is a credential handed to untrusted
			// code for no reason.
			AutomountServiceAccountToken: ptr(false),
			Containers: []corev1.Container{{
				Name:            "task",
				Image:           req.Image,
				ImagePullPolicy: pullPolicy,
				Env:             podEnv(req),
				Resources:       buildResources(req.Resources),
				SecurityContext: buildSecurityContext(req.PodSecurity),
				// Pin the termination-message contract explicitly rather than relying
				// on the Kubernetes default: the agent writes its durable outcome
				// record here before delivering the report, and the reconciler reads
				// it from pod status to recover a success whose report was lost (ADR
				// 0052). FallbackToLogsOnError is deliberately NOT set — it would
				// populate the message from the log tail, which is not a record.
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				TerminationMessagePath:   terminationMessagePath,
			}},
		},
	}
	if req.TimeoutSeconds > 0 {
		deadline := int64(req.TimeoutSeconds)
		pod.Spec.ActiveDeadlineSeconds = &deadline
	}
	if req.Execution.ServiceAccount != "" {
		pod.Spec.ServiceAccountName = req.Execution.ServiceAccount
	}
	mergeMetadata(pod.Labels, req.Execution.Labels)
	mergeMetadata(pod.Annotations, req.Execution.Annotations)
	mountStagingVolume(pod, req)
	mountAgentTLSCA(pod, req)
	mountTaskSecret(pod, req)
	return pod
}

// taskSecretVolumeName is the pod volume name for the operator-supplied task
// credential Secret.
const taskSecretVolumeName = "leoflow-task-secret"

// mountTaskSecret mounts an operator-configured Kubernetes Secret (when set)
// read-only into the task pod at req.TaskSecretMountPath. This is how a task
// reads a credential that lives in the cluster's secret store — e.g. a GCP
// service-account key a connection references by key_path — so Leoflow never
// stores the key itself (ADR 0035).
func mountTaskSecret(pod *corev1.Pod, req Request) {
	if req.TaskSecretName == "" || req.TaskSecretMountPath == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: taskSecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: req.TaskSecretName},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: taskSecretVolumeName, MountPath: req.TaskSecretMountPath, ReadOnly: true,
	})
}

// agentCAVolumeName / agentCADir / agentCAFile place the CA the agent uses to
// verify the control plane's gRPC server certificate (issue #58).
const (
	agentCAVolumeName = "leoflow-agent-ca"
	agentCADir        = "/etc/leoflow/tls"
	agentCAFile       = "ca.crt"
)

// mountAgentTLSCA mounts the CA ConfigMap (when configured) into the task pod so
// the agent can verify the control plane's TLS cert. The matching env vars are
// set in podEnv.
func mountAgentTLSCA(pod *corev1.Pod, req Request) {
	if req.AgentTLSCAConfigMap == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: agentCAVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: req.AgentTLSCAConfigMap},
			},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: agentCAVolumeName, MountPath: agentCADir, ReadOnly: true})
}

// stagingVolumeName is the pod volume name for the per-run staging PVC.
const stagingVolumeName = "leoflow-staging"

// stagingMountPath is where the per-run staging volume is mounted in task pods,
// exposed to user code as LEOFLOW_STAGING_DIR. See ADR 0022.
const stagingMountPath = "/staging"

// mountStagingVolume attaches the run's shared staging PVC (when set) to the task
// container at stagingMountPath, so a run's tasks share large intermediate data.
func mountStagingVolume(pod *corev1.Pod, req Request) {
	if req.StagingClaim == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: stagingVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: req.StagingClaim},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: stagingVolumeName, MountPath: stagingMountPath})
}

func podEnv(req Request) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 3+len(req.Env))
	env = append(env,
		corev1.EnvVar{Name: "LEOFLOW_CONTROL_PLANE_ADDR", Value: req.ControlPlaneAddr},
		corev1.EnvVar{Name: "LEOFLOW_AGENT_TOKEN", Value: req.AgentToken},
		corev1.EnvVar{Name: "LEOFLOW_TASK_INSTANCE_ID", Value: req.TaskInstanceID},
		// Tell the pod agent where to write its durable outcome record; it matches
		// the container's TerminationMessagePath. Only the pod path sets this, so
		// Lite (subprocess, no pod) leaves the agent's record writing disabled.
		corev1.EnvVar{Name: "LEOFLOW_TERMINATION_LOG_PATH", Value: terminationMessagePath},
	)
	if req.StagingClaim != "" {
		env = append(env, corev1.EnvVar{Name: "LEOFLOW_STAGING_DIR", Value: stagingMountPath})
	}
	if req.AgentTLSCAConfigMap != "" {
		env = append(env,
			corev1.EnvVar{Name: "LEOFLOW_AGENT_INSECURE", Value: "false"},
			corev1.EnvVar{Name: "LEOFLOW_AGENT_TLS_CA", Value: agentCADir + "/" + agentCAFile},
		)
	}
	for k, v := range req.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	return env
}

// ptr returns a pointer to v. The Kubernetes API models optional booleans as
// *bool, where nil means "cluster default" and is not the same as false.
func ptr[T any](v T) *T { return &v }

// buildSecurityContext hardens the task container toward Pod Security
// Admission's `restricted` profile, which requires four things: no privilege
// escalation, every capability dropped, a seccomp profile, and a non-root user.
//
// The first three are unconditional because they cost an ordinary task nothing:
// a task process does not escalate privileges, needs no Linux capability, and
// runs fine under the runtime's default seccomp filter. None of them depend on
// the UID, so they apply whatever the image runs as.
//
// The fourth, runAsNonRoot, is opt-in — see PodSecurity for why the images this
// repo ships cannot satisfy it yet. Until that flips, a `restricted` namespace
// still rejects task pods; what this buys today is every protection that does
// not require changing the images.
//
// readOnlyRootFilesystem is opt-in for a different reason: `restricted` never
// asks for it, and turning it on by default breaks any task that writes to /tmp.
// decodeStructured converts one untyped map — carried verbatim from the DAG spec
// — into a typed Kubernetes object via a JSON round-trip (the map keys match the
// target type's JSON field names). ok is false when the entry cannot be
// marshaled or decoded, so callers skip it rather than failing the whole pod
// build, matching how BuildPod treats other optional placement hints.
func decodeStructured[T any](m map[string]any) (out T, ok bool) {
	b, err := json.Marshal(m)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, false
	}
	return out, true
}

// decodeStructuredSlice maps decodeStructured over a slice of untyped entries,
// skipping malformed ones. It returns nil when none are declared or none survive,
// so the field stays unset for a DAG that declares none.
func decodeStructuredSlice[T any](raw []map[string]any) []T {
	if len(raw) == 0 {
		return nil
	}
	out := make([]T, 0, len(raw))
	for _, m := range raw {
		if v, ok := decodeStructured[T](m); ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildTolerations converts a task's declared tolerations — an untyped
// []map[string]any carried verbatim from the DAG spec — into typed pod
// tolerations via a JSON round-trip. A malformed entry is skipped and omission
// stays unset (nil).
func buildTolerations(raw []map[string]any) []corev1.Toleration {
	return decodeStructuredSlice[corev1.Toleration](raw)
}

// buildAffinity converts a task's declared affinity — an untyped map[string]any
// carried verbatim from the DAG spec — into a typed *corev1.Affinity via a JSON
// round-trip. Returns nil when none is declared or the entry is malformed, so the
// field stays unset for a DAG that declares none.
func buildAffinity(m map[string]any) *corev1.Affinity {
	if len(m) == 0 {
		return nil
	}
	if v, ok := decodeStructured[corev1.Affinity](m); ok {
		return &v
	}
	return nil
}

// mergeMetadata overlays operator-declared labels or annotations onto Leoflow's
// own pod metadata, but Leoflow's keys always win a collision: the leoflow.io/*
// identity labels and the task-instance-id annotation are load-bearing (the
// reconciler and terminate path select on them), so a DAG cannot shadow them. The
// own map is mutated in place; a nil declared map is a no-op.
func mergeMetadata(own, declared map[string]string) {
	for k, v := range declared {
		if _, taken := own[k]; !taken {
			own[k] = v
		}
	}
}

func buildSecurityContext(ps PodSecurity) *corev1.SecurityContext {
	sc := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if ps.RunAsNonRoot {
		sc.RunAsNonRoot = ptr(true)
	}
	if ps.ReadOnlyRootFilesystem {
		sc.ReadOnlyRootFilesystem = ptr(true)
	}
	return sc
}

func buildResources(r domain.Resources) corev1.ResourceRequirements {
	out := corev1.ResourceRequirements{}
	if r.Requests != nil {
		out.Requests = quantities(*r.Requests)
	}
	if r.Limits != nil {
		out.Limits = quantities(*r.Limits)
	}
	// Container-side Dynamic Resource Allocation: the claims (declared pod-level in
	// Execution.ResourceClaims) this container consumes (ADR 0054).
	out.Claims = decodeStructuredSlice[corev1.ResourceClaim](r.Claims)
	return out
}

func quantities(q domain.ResourceQuantity) corev1.ResourceList {
	list := corev1.ResourceList{}
	for name, value := range map[corev1.ResourceName]string{
		corev1.ResourceCPU:              q.CPU,
		corev1.ResourceMemory:           q.Memory,
		corev1.ResourceEphemeralStorage: q.EphemeralStorage,
	} {
		if value == "" {
			continue
		}
		if parsed, err := resource.ParseQuantity(value); err == nil {
			list[name] = parsed
		}
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

func podName(req Request) string {
	suffix := randSuffix()
	base := fmt.Sprintf("leoflow-%s-%s-%d", sanitizeLabel(req.DagID), sanitizeLabel(req.TaskID), req.TryNumber)
	maxBase := 63 - len(suffix) - 1
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return base + "-" + suffix
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func randSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}
