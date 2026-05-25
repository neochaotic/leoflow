package executor

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stagingLabel marks PVCs Leoflow manages for per-run staging, so GC can find
// them without touching anything else in the namespace.
const stagingLabel = "leoflow.io/staging"

// stagingRunIDAnnotation holds the raw (unsanitized) run_id, which GC uses to
// check run state — the run-id label is sanitized and may be lossy.
const stagingRunIDAnnotation = "leoflow.io/run-id"

// defaultStagingSize is used when a DAG enables staging without a size.
const defaultStagingSize = "1Gi"

// StagingClaimName is the deterministic PVC name for a run's staging volume. It
// must be stable across retries and clear+re-run so the same PVC is re-attached
// (ADR 0022), and DNS-safe.
func StagingClaimName(dagID, runID string) string {
	return fmt.Sprintf("leoflow-staging-%s-%s", sanitizeLabel(dagID), sanitizeLabel(runID))
}

// ensureStagingClaim creates the run's RWX staging PVC if it does not already
// exist (idempotent — called per task). An existing claim is reused, which is
// what makes a clear+re-run keep the upstream tasks' data.
func (e *KubernetesExecutor) ensureStagingClaim(ctx context.Context, req Request) error {
	size := req.StagingSize
	if size == "" {
		size = defaultStagingSize
	}
	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("invalid staging size %q: %w", size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: req.StagingClaim,
			Labels: map[string]string{
				stagingLabel:           "true",
				"leoflow.io/run-id":    sanitizeLabel(req.RunID),
				"leoflow.io/dag-id":    sanitizeLabel(req.DagID),
				"leoflow.io/tenant-id": sanitizeLabel(req.TenantID),
			},
			// The label is sanitized (run IDs contain label-illegal chars); GC needs
			// the raw run_id to check run state, so keep it as an annotation.
			Annotations: map[string]string{stagingRunIDAnnotation: req.RunID},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: qty}},
		},
	}
	if req.StagingStorageClass != "" {
		sc := req.StagingStorageClass
		pvc.Spec.StorageClassName = &sc
	}
	_, err = e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil // another task of the run already provisioned it
	}
	if err != nil {
		return fmt.Errorf("creating staging PVC %s: %w", req.StagingClaim, err)
	}
	return nil
}

// stagingTerminalAnnotation records when GC first observed a run terminal. The
// post-terminal TTL (ADR 0022) is measured from this stamp — not from PVC
// creation, which would expire the volume while the run was still active — and it
// is cleared if the run goes active again so a clear+re-run restarts the clock.
const stagingTerminalAnnotation = "leoflow.io/terminal-since"

// GCStagingClaims deletes staging PVCs whose run has been terminal for longer
// than ttl. isTerminal reports whether a run has finished; the TTL is measured
// from when the run first became terminal (ADR 0022's "24h post-terminal TTL"),
// the grace window so a clear+re-run shortly after a failure still finds the
// data. It mirrors the pod Reconciler.
func (e *KubernetesExecutor) GCStagingClaims(ctx context.Context, isTerminal func(runID string) bool, ttl time.Duration) error {
	list, err := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: stagingLabel + "=true",
	})
	if err != nil {
		return fmt.Errorf("listing staging PVCs: %w", err)
	}
	now := time.Now()
	for i := range list.Items {
		pvc := &list.Items[i]
		runID := pvc.Annotations[stagingRunIDAnnotation]
		stamp := pvc.Annotations[stagingTerminalAnnotation]
		if !isTerminal(runID) {
			if stamp != "" { // re-activated: restart the post-terminal clock
				if serr := e.setStagingTerminalStamp(ctx, pvc.Name, ""); serr != nil {
					return serr
				}
			}
			continue
		}
		since, ok := parseStagingStamp(stamp)
		if !ok {
			// First sweep that sees the run terminal (or a corrupt stamp): stamp
			// now so the TTL starts at terminal, and keep the volume this round.
			if serr := e.setStagingTerminalStamp(ctx, pvc.Name, now.UTC().Format(time.RFC3339)); serr != nil {
				return serr
			}
			continue
		}
		if now.Sub(since) <= ttl {
			continue
		}
		if derr := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return fmt.Errorf("deleting staging PVC %s: %w", pvc.Name, derr)
		}
	}
	return nil
}

// parseStagingStamp parses the terminal-since annotation, reporting whether it
// held a usable timestamp.
func parseStagingStamp(stamp string) (time.Time, bool) {
	if stamp == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// setStagingTerminalStamp sets (value != "") or clears (value == "") the
// terminal-since annotation on a staging PVC. A missing PVC is not an error.
func (e *KubernetesExecutor) setStagingTerminalStamp(ctx context.Context, name, value string) error {
	pvc, err := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading staging PVC %s: %w", name, err)
	}
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	if value == "" {
		delete(pvc.Annotations, stagingTerminalAnnotation)
	} else {
		pvc.Annotations[stagingTerminalAnnotation] = value
	}
	if _, err := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("stamping staging PVC %s: %w", name, err)
	}
	return nil
}
