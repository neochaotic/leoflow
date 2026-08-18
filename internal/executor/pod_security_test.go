package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Pod Security Admission's `restricted` profile requires four things of a
// container: no privilege escalation, every capability dropped, a seccomp
// profile, and a non-root user. The first three cost an ordinary task nothing
// and are applied unconditionally; the fourth reaches BuildPod through
// req.PodSecurity — on by default now that the shipped task images carry numeric
// non-root UIDs (runtime/Dockerfile: USER 65532:65532), and paired with a
// pod-level fsGroup so the non-root user can write its mounted volumes.
//
// readOnlyRootFilesystem is not part of `restricted` at all, and it is the field
// most likely to break an ordinary Python task (pip cache, /tmp, matplotlib
// config). Opt-in for that reason, not this one.

// wantNonRootFSGroup mirrors the GID the task base image runs as
// (runtime/Dockerfile: USER 65532:65532). BuildPod carries it as the pod's
// fsGroup for a non-root task so the kubelet makes mounted volumes — the per-run
// staging PVC above all (ADR 0022) — group-writable by that user.
const wantNonRootFSGroup int64 = 65532

func TestBuildPodAppliesUnconditionalHardening(t *testing.T) {
	pod := BuildPod(sampleReq())
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(pod.Spec.Containers))
	}
	sc := pod.Spec.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("container SecurityContext is nil — PSA restricted rejects the pod outright")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation must be false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop = %v, want [ALL]", sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("SeccompProfile = %v, want RuntimeDefault", sc.SeccompProfile)
	}
}

// The task pod talks to the control plane over gRPC with its own per-task token
// and never calls the Kubernetes API, so mounting a ServiceAccount token only
// hands untrusted code a credential it has no use for.
func TestBuildPodDoesNotMountServiceAccountToken(t *testing.T) {
	pod := BuildPod(sampleReq())
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken must be false: the task never calls the Kubernetes API")
	}
}

// readOnlyRootFilesystem stays off unless the DAG asks for it, for the reason in
// the file comment. This pins that choice so a later "harden everything" pass
// does not flip it silently and break every task that writes to /tmp.
func TestBuildPodLeavesRootFilesystemWritableByDefault(t *testing.T) {
	pod := BuildPod(sampleReq())
	sc := pod.Spec.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if sc.ReadOnlyRootFilesystem != nil && *sc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem must default to off: PSA restricted does not require it " +
			"and it breaks ordinary Python tasks")
	}
}

// BuildPod is pure: it sets runAsNonRoot only when the request asks for it and
// never invents it. Whether a task runs non-root by default is a config/Helm
// decision — on by default now — that reaches BuildPod through req.PodSecurity,
// so a request that leaves it unset must still produce a pod without
// runAsNonRoot (and, being potentially root, without an fsGroup to fix up).
func TestBuildPodLeavesRunAsNonRootUnsetWhenNotRequested(t *testing.T) {
	pod := BuildPod(sampleReq())
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
		t.Error("RunAsNonRoot must not be set when the request does not ask for it")
	}
	// The protections that cost nothing apply regardless of the UID.
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation must be false whatever the image runs as")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 {
		t.Error("capabilities must be dropped whatever the image runs as")
	}
}

// A non-root request completes the `restricted` set and pairs runAsNonRoot with
// a pod-level fsGroup, so the non-root user can write the per-run staging PVC
// (ADR 0022) instead of landing locked out of a root-owned volume.
func TestBuildPodHonorsRunAsNonRootAndSetsFSGroup(t *testing.T) {
	req := sampleReq()
	req.PodSecurity.RunAsNonRoot = true
	pod := BuildPod(req)
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("RunAsNonRoot was not honored")
	}
	psc := pod.Spec.SecurityContext
	if psc == nil || psc.FSGroup == nil {
		t.Fatal("pod SecurityContext.FSGroup must be set for a non-root task so its mounted volumes are group-writable")
	}
	if *psc.FSGroup != wantNonRootFSGroup {
		t.Errorf("FSGroup = %d, want %d (task base image GID)", *psc.FSGroup, wantNonRootFSGroup)
	}
}

// A task that may run as root writes its volumes as root, so there is nothing
// for an fsGroup to fix. BuildPod leaves the pod SecurityContext unset — keeping
// the spec byte-identical to before this feature and skipping the kubelet's
// recursive volume chown.
func TestBuildPodOmitsFSGroupWhenNotNonRoot(t *testing.T) {
	pod := BuildPod(sampleReq())
	if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.FSGroup != nil {
		t.Errorf("FSGroup must be unset when the task may run as root, got %d", *pod.Spec.SecurityContext.FSGroup)
	}
}

// A DAG that asks for a read-only root filesystem gets one.
func TestBuildPodHonorsReadOnlyRootFilesystemOptIn(t *testing.T) {
	req := sampleReq()
	req.PodSecurity.ReadOnlyRootFilesystem = true
	pod := BuildPod(req)
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem opt-in was not honored")
	}
}
