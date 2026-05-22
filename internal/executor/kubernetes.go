package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
}

// NewKubernetesExecutor builds an executor creating pods in the given namespace.
func NewKubernetesExecutor(clientset kubernetes.Interface, namespace string) *KubernetesExecutor {
	if namespace == "" {
		namespace = "default"
	}
	return &KubernetesExecutor{clientset: clientset, namespace: namespace}
}

// Execute creates the task pod. The agent inside the pod reports state over gRPC.
func (e *KubernetesExecutor) Execute(ctx context.Context, req Request) error {
	if req.Operator == "http_api" {
		return fmt.Errorf("execution_mode: pod for http_api is not yet implemented; "+
			"use execution_mode: inline (default) for short-lived calls, or wait for v0.2 (task %s)", req.TaskID)
	}
	pod := BuildPod(req)
	if _, err := e.clientset.CoreV1().Pods(e.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating pod for task %s: %w", req.TaskID, err)
	}
	return nil
}

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
			Containers: []corev1.Container{{
				Name:            "task",
				Image:           req.Image,
				ImagePullPolicy: pullPolicy,
				Env:             podEnv(req),
				Resources:       buildResources(req.Resources),
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
	return pod
}

func podEnv(req Request) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 3+len(req.Env))
	env = append(env,
		corev1.EnvVar{Name: "LEOFLOW_CONTROL_PLANE_ADDR", Value: req.ControlPlaneAddr},
		corev1.EnvVar{Name: "LEOFLOW_AGENT_TOKEN", Value: req.AgentToken},
		corev1.EnvVar{Name: "LEOFLOW_TASK_INSTANCE_ID", Value: req.TaskInstanceID},
	)
	for k, v := range req.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	return env
}

func buildResources(r domain.Resources) corev1.ResourceRequirements {
	out := corev1.ResourceRequirements{}
	if r.Requests != nil {
		out.Requests = quantities(r.Requests.CPU, r.Requests.Memory)
	}
	if r.Limits != nil {
		out.Limits = quantities(r.Limits.CPU, r.Limits.Memory)
	}
	return out
}

func quantities(cpu, memory string) corev1.ResourceList {
	list := corev1.ResourceList{}
	if q, err := resource.ParseQuantity(cpu); err == nil && cpu != "" {
		list[corev1.ResourceCPU] = q
	}
	if q, err := resource.ParseQuantity(memory); err == nil && memory != "" {
		list[corev1.ResourceMemory] = q
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
