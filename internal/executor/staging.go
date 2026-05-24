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

// GCStagingClaims deletes staging PVCs whose run is terminal and which are older
// than ttl. isTerminal reports whether a run has finished; the TTL is the grace
// window so a clear+re-run shortly after a failure still finds the data (ADR
// 0022). It mirrors the pod Reconciler.
func (e *KubernetesExecutor) GCStagingClaims(ctx context.Context, isTerminal func(runID string) bool, ttl time.Duration) error {
	list, err := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: stagingLabel + "=true",
	})
	if err != nil {
		return fmt.Errorf("listing staging PVCs: %w", err)
	}
	cutoff := time.Now().Add(-ttl)
	for i := range list.Items {
		pvc := &list.Items[i]
		runID := pvc.Annotations[stagingRunIDAnnotation]
		if !isTerminal(runID) || pvc.CreationTimestamp.After(cutoff) {
			continue
		}
		if err := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting staging PVC %s: %w", pvc.Name, err)
		}
	}
	return nil
}
