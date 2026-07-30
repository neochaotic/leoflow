package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Pod Security Admission's `restricted` profile requires exactly four things of
// a container: it must not run as root, it must not allow privilege escalation,
// it must drop every capability, and it must carry a seccomp profile. A cluster
// with `restricted` enforced on the task namespace REJECTS pods that omit them —
// so without these the executor cannot place a task at all, which makes this an
// admission blocker rather than hardening.
//
// readOnlyRootFilesystem is deliberately NOT in this list. `restricted` does not
// require it, and it is the single field most likely to break an ordinary Python
// task (pip cache, /tmp, matplotlib config). Setting it by default would cost
// compatibility for no admission benefit; it stays opt-in.

func TestBuildPodSatisfiesRestrictedPSA(t *testing.T) {
	pod := BuildPod(sampleReq())
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(pod.Spec.Containers))
	}
	sc := pod.Spec.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("container SecurityContext is nil — PSA restricted rejects the pod outright")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("RunAsNonRoot must be true: untrusted task code must not run as root")
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

// An image that genuinely needs root (a legacy base, a task that installs
// packages at runtime) must stay runnable. The escape hatch is explicit and
// per-request, so choosing it is a visible decision rather than a silent default.
func TestBuildPodAllowsOptingOutOfRunAsNonRoot(t *testing.T) {
	req := sampleReq()
	req.PodSecurity.AllowRoot = true
	pod := BuildPod(req)
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
		t.Error("AllowRoot must clear RunAsNonRoot so a root image still runs")
	}
	// Everything that costs nothing stays on even when root is allowed: dropping
	// capabilities and blocking escalation do not depend on the UID.
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation must stay false even when root is allowed")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 {
		t.Error("capabilities must still be dropped even when root is allowed")
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
