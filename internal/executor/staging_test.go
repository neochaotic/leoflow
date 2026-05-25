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

// The TTL is measured from when the run became terminal (ADR 0022: "24h
// post-terminal TTL"), not from PVC creation. GC stamps a PVC the first sweep it
// sees the run terminal, deletes it once that stamp ages past the TTL, and clears
// the stamp if the run goes active again (clear+re-run restarts the clock).
func TestGCStagingClaims(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	oldStamp := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	recentStamp := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	created48h := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	mk := func(name, runID string, terminalSince string) {
		ann := map[string]string{"leoflow.io/run-id": runID}
		if terminalSince != "" {
			ann[stagingTerminalAnnotation] = terminalSince
		}
		_, _ = cs.CoreV1().PersistentVolumeClaims("leoflow").Create(context.Background(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, CreationTimestamp: created48h, // all old by creation; only terminal time matters
				Labels:      map[string]string{"leoflow.io/staging": "true"},
				Annotations: ann,
			},
		}, metav1.CreateOptions{})
	}
	mk("s-terminal-stamped-old", "run-done-old", oldStamp)    // terminal > TTL ago -> delete
	mk("s-terminal-stamped-new", "run-done-new", recentStamp) // terminal recently -> keep
	mk("s-terminal-unstamped", "run-just-done", "")           // first sweep: stamp + keep (even though created 48h ago)
	mk("s-running-stale-stamp", "run-active", oldStamp)       // re-activated -> keep + clear stamp

	terminal := map[string]bool{"run-done-old": true, "run-done-new": true, "run-just-done": true}
	gc := func() error {
		return e.GCStagingClaims(context.Background(), func(runID string) bool { return terminal[runID] }, 24*time.Hour)
	}
	if err := gc(); err != nil {
		t.Fatalf("GC: %v", err)
	}
	get := func(name string) *corev1.PersistentVolumeClaim {
		p, err := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return nil
		}
		return p
	}
	if get("s-terminal-stamped-old") != nil {
		t.Error("terminal PVC stamped past TTL should be deleted")
	}
	if get("s-terminal-stamped-new") == nil {
		t.Error("terminal PVC stamped within TTL should be kept")
	}
	// Unstamped terminal PVC: kept this sweep, but now stamped (so it is NOT
	// deleted just for being old — the clock starts at terminal, not creation).
	if p := get("s-terminal-unstamped"); p == nil {
		t.Error("unstamped terminal PVC should be kept on first sweep")
	} else if p.Annotations[stagingTerminalAnnotation] == "" {
		t.Error("unstamped terminal PVC should be stamped with the terminal time")
	}
	// Re-activated run: kept, and its stale terminal stamp cleared.
	if p := get("s-running-stale-stamp"); p == nil {
		t.Error("active run's PVC should be kept")
	} else if p.Annotations[stagingTerminalAnnotation] != "" {
		t.Error("re-activated run's PVC should have its terminal stamp cleared")
	}
}
