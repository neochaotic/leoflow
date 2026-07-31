package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Pod Security Admission's `restricted` profile requires four things of a
// container: no privilege escalation, every capability dropped, a seccomp
// profile, and a non-root user. The first three cost an ordinary task nothing
// and are applied unconditionally; the fourth is opt-in until the images this
// repo ships can satisfy it (see PodSecurity.RunAsNonRoot).
//
// readOnlyRootFilesystem is not part of `restricted` at all, and it is the field
// most likely to break an ordinary Python task (pip cache, /tmp, matplotlib
// config). Opt-in for that reason, not this one.

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

// runAsNonRoot stays off unless asked for. Every examples/*/Dockerfile in this
// repo runs as root and runtime/Dockerfile declares a non-numeric USER, so
// defaulting it on would stop every shipped example from scheduling. This pins
// the sequencing: fix the images first, then flip the default.
func TestBuildPodLeavesRunAsNonRootOptIn(t *testing.T) {
	pod := BuildPod(sampleReq())
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
		t.Error("RunAsNonRoot must stay opt-in until the shipped images carry numeric non-root UIDs")
	}
	// The protections that cost nothing apply regardless of the UID.
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation must be false whatever the image runs as")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 {
		t.Error("capabilities must be dropped whatever the image runs as")
	}
}

// An operator whose images do carry numeric non-root UIDs can complete the
// `restricted` set.
func TestBuildPodHonorsRunAsNonRootOptIn(t *testing.T) {
	req := sampleReq()
	req.PodSecurity.RunAsNonRoot = true
	pod := BuildPod(req)
	sc := pod.Spec.Containers[0].SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("RunAsNonRoot opt-in was not honored")
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
