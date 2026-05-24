package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStagingClaimName(t *testing.T) {
	n := StagingClaimName("ETL Vendas", "manual__2026-05-23T21:00:00Z")
	if n == "" || n[:16] != "leoflow-staging-" {
		t.Fatalf("name = %q, want leoflow-staging- prefix", n)
	}
	// Deterministic: same inputs -> same name (so a re-run re-attaches the PVC).
	if n != StagingClaimName("ETL Vendas", "manual__2026-05-23T21:00:00Z") {
		t.Error("claim name must be deterministic")
	}
	// DNS-safe: lowercase, no spaces/colons.
	for _, c := range n {
		if c == ' ' || c == ':' || (c >= 'A' && c <= 'Z') {
			t.Errorf("name %q has a non-DNS-safe char %q", n, c)
		}
	}
}

func TestEnsureStagingClaimIsIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := sampleReq()
	req.StagingClaim = "leoflow-staging-etl-r1"
	req.StagingSize = "5Gi"

	for i := 0; i < 2; i++ { // called per task; must not error on the second
		if err := e.ensureStagingClaim(context.Background(), req); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), req.StagingClaim, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not created: %v", err)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("accessModes = %v, want [ReadWriteMany]", pvc.Spec.AccessModes)
	}
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "5Gi" {
		t.Errorf("size = %s, want 5Gi", got)
	}
	if pvc.Labels["leoflow.io/run-id"] == "" {
		t.Error("PVC must carry the run-id label for GC")
	}
}

func TestGCStagingClaims(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	old := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	recent := metav1.NewTime(time.Now())
	mk := func(name, runID string, created metav1.Time) {
		_, _ = cs.CoreV1().PersistentVolumeClaims("leoflow").Create(context.Background(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, CreationTimestamp: created,
				Labels: map[string]string{"leoflow.io/staging": "true", "leoflow.io/run-id": runID},
			},
		}, metav1.CreateOptions{})
	}
	mk("s-terminal-old", "run-done-old", old)    // terminal + past TTL -> delete
	mk("s-terminal-new", "run-done-new", recent) // terminal but within TTL -> keep
	mk("s-running", "run-active", old)           // not terminal -> keep

	terminal := map[string]bool{"run-done-old": true, "run-done-new": true}
	if err := e.GCStagingClaims(context.Background(), func(runID string) bool { return terminal[runID] }, 24*time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}
	left, _ := cs.CoreV1().PersistentVolumeClaims("leoflow").List(context.Background(), metav1.ListOptions{})
	names := map[string]bool{}
	for _, p := range left.Items {
		names[p.Name] = true
	}
	if names["s-terminal-old"] {
		t.Error("terminal PVC past TTL should be deleted")
	}
	if !names["s-terminal-new"] || !names["s-running"] {
		t.Errorf("kept set wrong: %v (want s-terminal-new + s-running)", names)
	}
}
