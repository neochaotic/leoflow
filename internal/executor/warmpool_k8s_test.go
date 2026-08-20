package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestKubernetesWarmPodsListSelectsWarmOnly proves the cluster-backed client lists
// ONLY warm-worker pods (by label) and reads the dag_version each serves, so a
// namespace full of ordinary task pods does not confuse the reconciler's counts.
func TestKubernetesWarmPodsListSelectsWarmOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "warm-1", Namespace: "leoflow",
			Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "task-1", Namespace: "leoflow",
			Labels: map[string]string{"leoflow.io/run-id": "r1"},
		}},
	)
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d warm pods, want 1 (task pods must be excluded): %+v", len(got), got)
	}
	if got[0].Name != "warm-1" || got[0].DagVersionID != "dv1" {
		t.Errorf("listed %+v, want {warm-1 dv1}", got[0])
	}
}

// TestKubernetesWarmPodsCreateBuildsAndCreates proves CreateWarmPod runs the
// injected spec builder, builds a warm pod, and Creates it in the namespace.
func TestKubernetesWarmPodsCreateBuildsAndCreates(t *testing.T) {
	cs := fake.NewSimpleClientset()
	newSpec := func(t WarmTarget) (WarmPodSpec, error) {
		return WarmPodSpec{DagVersionID: t.DagVersionID, Image: t.Image, BootstrapToken: "tok"}, nil
	}
	k := NewKubernetesWarmPods(cs, "leoflow", newSpec)
	if err := k.CreateWarmPod(context.Background(), WarmTarget{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 1}); err != nil {
		t.Fatalf("CreateWarmPod: %v", err)
	}
	pods, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{
		LabelSelector: warmWorkerLabelKey + "=" + warmWorkerLabelVal,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("created %d warm pods, want 1", len(pods.Items))
	}
	if got := pods.Items[0].Spec.Containers[0].Image; got != "img" {
		t.Errorf("created pod image = %q, want img", got)
	}
}

// TestKubernetesWarmPodsDelete proves DeleteWarmPod removes the named pod.
func TestKubernetesWarmPodsDelete(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "warm-1", Namespace: "leoflow",
		Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
	}})
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	if err := k.DeleteWarmPod(context.Background(), "warm-1"); err != nil {
		t.Fatalf("DeleteWarmPod: %v", err)
	}
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("after delete listed %d, want 0", len(got))
	}
}
