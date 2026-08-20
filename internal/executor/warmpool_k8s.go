package executor

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WarmPodSpecFunc mints the per-pod bootstrap credential and fills the transport /
// connection / security fields for a warm worker of the target, returning a spec
// ready for BuildWarmPod. It is the auth- and config-aware half of warm-pod
// creation, injected from main.go so the executor package imports neither auth nor
// config. It is called once per warm worker the reconciler needs to create.
type WarmPodSpecFunc func(t WarmTarget) (WarmPodSpec, error)

// KubernetesWarmPods is the production WarmPodClient: it lists warm pods by the
// warm-worker label, builds new ones via the injected spec func + BuildWarmPod,
// and deletes them, all in one namespace. It owns the label selector so the
// executor's warm-worker label contract stays private to this package.
type KubernetesWarmPods struct {
	clientset kubernetes.Interface
	namespace string
	newSpec   WarmPodSpecFunc
}

// NewKubernetesWarmPods builds the cluster-backed warm-pod client. newSpec is the
// auth/config-aware builder invoked per create; List and Delete do not need it.
func NewKubernetesWarmPods(cs kubernetes.Interface, namespace string, newSpec WarmPodSpecFunc) *KubernetesWarmPods {
	if namespace == "" {
		namespace = "default"
	}
	return &KubernetesWarmPods{clientset: cs, namespace: namespace, newSpec: newSpec}
}

// warmPodSelector selects exactly the warm-worker pods (excludes ordinary task
// pods), the label BuildWarmPod stamps.
const warmPodSelector = warmWorkerLabelKey + "=" + warmWorkerLabelVal

// ListWarmPods returns every warm-worker pod in the namespace, tagged with the
// dag_version it serves (from its label).
func (k *KubernetesWarmPods) ListWarmPods(ctx context.Context) ([]WarmPodInfo, error) {
	list, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{LabelSelector: warmPodSelector})
	if err != nil {
		return nil, fmt.Errorf("listing warm pods: %w", err)
	}
	out := make([]WarmPodInfo, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		out = append(out, WarmPodInfo{Name: p.Name, DagVersionID: p.Labels[warmDagVersionLabelKey]})
	}
	return out, nil
}

// CreateWarmPod mints the target's warm-pod spec, builds the pod, and creates it.
func (k *KubernetesWarmPods) CreateWarmPod(ctx context.Context, t WarmTarget) error {
	if k.newSpec == nil {
		return fmt.Errorf("warm pod creation requires a spec builder")
	}
	spec, err := k.newSpec(t)
	if err != nil {
		return fmt.Errorf("building warm pod spec for dag_version %s: %w", t.DagVersionID, err)
	}
	spec.Namespace = k.namespace
	pod := BuildWarmPod(spec)
	if _, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating warm pod for dag_version %s: %w", t.DagVersionID, err)
	}
	return nil
}

// DeleteWarmPod removes one warm worker by name.
func (k *KubernetesWarmPods) DeleteWarmPod(ctx context.Context, name string) error {
	if err := k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("deleting warm pod %s: %w", name, err)
	}
	return nil
}

var _ WarmPodClient = (*KubernetesWarmPods)(nil)
