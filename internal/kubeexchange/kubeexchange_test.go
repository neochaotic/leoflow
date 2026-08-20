package kubeexchange

import (
	"context"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/neochaotic/leoflow/internal/agentrpc"
	"github.com/neochaotic/leoflow/internal/executor"
)

// TestReviewedPodFromStatus parses the bound-token TokenReview status into a
// ReviewedPod: an authenticated review yields the pod name/uid from the apiserver
// "extra" keys and the namespace from the ServiceAccount username (ADR 0055 D7).
func TestReviewedPodFromStatus(t *testing.T) {
	st := authv1.TokenReviewStatus{
		Authenticated: true,
		User: authv1.UserInfo{
			Username: "system:serviceaccount:leoflow:task",
			Extra: map[string]authv1.ExtraValue{
				boundTokenPodNameKey: {"leoflow-etl-extract-1-abcd"},
				boundTokenPodUIDKey:  {"uid-123"},
			},
		},
	}
	pod, err := reviewedPodFromStatus(st)
	if err != nil {
		t.Fatalf("reviewedPodFromStatus: %v", err)
	}
	if pod.PodName != "leoflow-etl-extract-1-abcd" || pod.PodUID != "uid-123" {
		t.Errorf("pod name/uid = %q/%q", pod.PodName, pod.PodUID)
	}
	if pod.Namespace != "leoflow" {
		t.Errorf("namespace = %q, want leoflow (from SA username)", pod.Namespace)
	}
}

// TestReviewedPodFromStatusRejectsUnauthenticated: authenticated=false (a bad,
// expired, or wrong-audience token — the apiserver sets this) is an error, never
// a ReviewedPod.
func TestReviewedPodFromStatusRejectsUnauthenticated(t *testing.T) {
	if _, err := reviewedPodFromStatus(authv1.TokenReviewStatus{Authenticated: false, Error: "bad audience"}); err == nil {
		t.Error("an unauthenticated review must be an error")
	}
}

// TestReviewedPodFromStatusRejectsMissingPod: an authenticated token that is not
// pod-bound (no pod-name extra) cannot resolve to a task instance — reject rather
// than mint a token for an ambiguous caller.
func TestReviewedPodFromStatusRejectsMissingPod(t *testing.T) {
	st := authv1.TokenReviewStatus{Authenticated: true, User: authv1.UserInfo{Username: "system:serviceaccount:leoflow:task"}}
	if _, err := reviewedPodFromStatus(st); err == nil {
		t.Error("a non-pod-bound token must be an error")
	}
}

// TestPodResolverReadsIdentityAnnotation: the resolver reads the exact identity
// the executor stamped on the pod (via the shared AgentIdentityAnnotation
// contract), so scoping/liveness key on the true attempt — not lossy labels.
func TestPodResolverReadsIdentityAnnotation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "leoflow-etl-extract-1-abcd",
		Namespace: "leoflow",
		UID:       "uid-123",
		Annotations: map[string]string{
			executor.AgentIdentityAnnotation: `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2}`,
		},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")

	id, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{Namespace: "leoflow", PodName: "leoflow-etl-extract-1-abcd", PodUID: "uid-123"})
	if err != nil {
		t.Fatalf("ResolveTaskInstance: %v", err)
	}
	if id.TaskInstanceID != "ti-1" || id.TenantID != "acme" || id.DagID != "ETL" || id.TaskID != "Extract" || id.TryNumber != 2 {
		t.Errorf("resolved identity = %+v", id)
	}
}

// TestPodResolverRejectsStaleUID: a name reused by a different pod (UID mismatch)
// must not resolve — the reviewed token was issued for a different pod object.
func TestPodResolverRejectsStaleUID(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "leoflow-etl-extract-1-abcd", Namespace: "leoflow", UID: "current-uid",
		Annotations: map[string]string{executor.AgentIdentityAnnotation: `{"ti":"ti-1"}`},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")
	if _, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{PodName: "leoflow-etl-extract-1-abcd", PodUID: "stale-uid"}); err == nil {
		t.Error("a UID mismatch must be an error")
	}
}

// TestPodResolverRejectsMissingAnnotation: a pod without the identity annotation
// (e.g. an env-var-transport pod) cannot be resolved via the exchange path.
func TestPodResolverRejectsMissingAnnotation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "leoflow", UID: "u"}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")
	if _, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{PodName: "p", PodUID: "u"}); err == nil {
		t.Error("a pod without the identity annotation must be an error")
	}
}
