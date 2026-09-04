package executor

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// TestBuildPodFloorsActiveDeadlineWhenNoTimeout: a task whose DAG declares no
// execution timeout still gets an ActiveDeadlineSeconds, derived from the
// attempt credential ceiling. The agent's reports retry for as long as the
// control plane is down, so without a floor a total control-plane outage would
// leave every such pod Running forever, holding its requests and blocking node
// scale-down. Past the credential ceiling the pod can do nothing useful anyway:
// heartbeat renewal stops there and its bearer lapses.
func TestBuildPodFloorsActiveDeadlineWhenNoTimeout(t *testing.T) {
	req := sampleReq()
	req.TimeoutSeconds = 0
	req.AttemptLifetimeCeilingSeconds = 86400

	pod := BuildPod(req)
	if pod.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("a task pod with no declared timeout must still carry a deadline floor")
	}
	if got := *pod.Spec.ActiveDeadlineSeconds; got != 86400 {
		t.Errorf("activeDeadlineSeconds = %d, want the credential ceiling 86400", got)
	}
}

// TestBuildPodUserTimeoutWinsOverFloor: a user-declared timeout is never
// shortened by the floor — whether it is below the ceiling (the common case) or
// above it (the user has explicitly asked for a longer bound, which is theirs to
// make). The deadline is derived from the user's timeout (plus the startup
// headroom and grace that keep the agent the enforcer), never from the ceiling.
func TestBuildPodUserTimeoutWinsOverFloor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout int
		ceiling int64
	}{
		{"timeout below ceiling", 600, 86400},
		{"timeout above ceiling", 172800, 86400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleReq()
			req.TimeoutSeconds = tc.timeout
			req.AttemptLifetimeCeilingSeconds = tc.ceiling
			want := int64(tc.timeout) + int64(defaultDispatchLostThreshold/time.Second) + corev1.DefaultTerminationGracePeriodSeconds
			got := BuildPod(req).Spec.ActiveDeadlineSeconds
			if got == nil {
				t.Fatal("a declared timeout must produce a pod deadline")
			}
			if *got != want {
				t.Errorf("activeDeadlineSeconds = %d, want %d — derived from the user's %d, not the ceiling %d",
					*got, want, tc.timeout, tc.ceiling)
			}
		})
	}
}

// TestBuildPodNoDeadlineWhenCeilingDisabled: with no declared timeout AND the
// credential ceiling disabled (non-positive, the operator's documented "no
// ceiling"), the pod carries no deadline — the same zero-means-unbounded
// convention the warm worker's attempt watchdog follows for the same knob.
func TestBuildPodNoDeadlineWhenCeilingDisabled(t *testing.T) {
	req := sampleReq()
	req.TimeoutSeconds = 0
	req.AttemptLifetimeCeilingSeconds = 0
	if pod := BuildPod(req); pod.Spec.ActiveDeadlineSeconds != nil {
		t.Errorf("activeDeadlineSeconds = %d, want none when both timeout and ceiling are unset", *pod.Spec.ActiveDeadlineSeconds)
	}
}

// TestBuildPodDeadlineOutlastsDeclaredTimeout locks the layering that keeps the
// agent, not the kubelet, the enforcer of a declared execution_timeout. The
// kubelet counts activeDeadlineSeconds from pod.Status.StartTime — stamped
// before the image pull — while the agent's own deadline starts after the
// RUNNING pre-flight, source staging and venv resolution, so a deadline equal to
// the declared timeout makes the kubelet win by the whole startup cost every
// time (systematically, not as a race) and the operator gets a generic kubelet
// reason instead of the timeout diagnosis. The deadline must therefore be
// strictly longer than the timeout, by the startup headroom plus the pod's own
// termination grace (grace alone would cover the tail, not the head).
func TestBuildPodDeadlineOutlastsDeclaredTimeout(t *testing.T) {
	headroom := int64(defaultDispatchLostThreshold / time.Second)
	for _, tc := range []struct {
		name  string
		grace *int64
		want  int64
	}{
		{"declared grace", ptr(int64(45)), 600 + 180 + 45},
		{"default grace", nil, 600 + 180 + corev1.DefaultTerminationGracePeriodSeconds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleReq() // TimeoutSeconds: 600
			req.Execution.TerminationGracePeriodSeconds = tc.grace
			got := BuildPod(req).Spec.ActiveDeadlineSeconds
			if got == nil {
				t.Fatal("a declared timeout must still produce a pod deadline")
			}
			if *got <= int64(req.TimeoutSeconds) {
				t.Fatalf("activeDeadlineSeconds = %d, must exceed the declared timeout %d "+
					"so the agent's clock fires first", *got, req.TimeoutSeconds)
			}
			if *got != tc.want {
				t.Errorf("activeDeadlineSeconds = %d, want %d (timeout + startup headroom %d + grace)",
					*got, tc.want, headroom)
			}
		})
	}
}

// TestPodDeadlineLetsTheAgentTimeoutFirst asserts the two halves of the
// operator-facing invariant: the reason a timed-out task carries is the agent's
// `execution_timeout: task exceeded Xs limit`, never the kubelet's. A true
// end-to-end assertion needs a cluster (a real kubelet stamping StartTime and
// killing the pod), so the halves are asserted separately: here that the pod
// deadline leaves the agent room to fire and deliver its report even on the
// slowest startup the control plane still considers healthy, and in
// internal/agent (TestRunnerEnforcesExecutionTimeout) that the agent's own
// deadline reports exactly that reason.
func TestPodDeadlineLetsTheAgentTimeoutFirst(t *testing.T) {
	req := sampleReq() // TimeoutSeconds: 600
	deadline := BuildPod(req).Spec.ActiveDeadlineSeconds
	if deadline == nil {
		t.Fatal("a declared timeout must produce a pod deadline")
	}
	// Worst healthy case: the agent's clock starts a full dispatch-lost threshold
	// after the kubelet's — a pod still inside that window is one the control
	// plane itself treats as healthily starting. The agent then fires at
	// headroom+timeout on the kubelet's clock, and must still have the pod's
	// termination grace left to shut the child down and deliver its report.
	headroom := int64(defaultDispatchLostThreshold / time.Second)
	agentFiresAt := headroom + int64(req.TimeoutSeconds)
	if slack := *deadline - agentFiresAt; slack < corev1.DefaultTerminationGracePeriodSeconds {
		t.Errorf("pod deadline %d leaves the agent only %ds after its own deadline at %ds; "+
			"the kubelet must not preempt the agent's report", *deadline, slack, agentFiresAt)
	}
	// The other half of the bug class: when the kubelet DOES win, this is what the
	// operator gets — no mention of the declared timeout, nothing actionable.
	killed := &corev1.Pod{Status: corev1.PodStatus{
		Phase:  corev1.PodFailed,
		Reason: "DeadlineExceeded",
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  taskContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "DeadlineExceeded", ExitCode: 137}},
		}},
	}}
	if reason := podFailureReason(killed); strings.Contains(reason, "execution_timeout") {
		t.Errorf("podFailureReason = %q; the kubelet cannot diagnose a declared timeout, "+
			"which is why it must never fire first", reason)
	}
}

// TestBuildPodDeadlineCapsTerminationGraceTerm bounds the overshoot a DAG can
// buy itself through the grace term (#910 review F2).
//
// Execution.TerminationGracePeriodSeconds is unvalidated — a bare *int64 in
// internal/domain — so a DAG may declare 3600. Added verbatim, that pushes the
// pod's deadline an hour past the declared timeout, and the kubelet then grants
// the same hour of SIGTERM grace again on top: the pod outlives its declared
// execution_timeout by about two hours. The term is still right to ADD (it
// budgets the agent's own tail after its clock fires), but it is capped, because
// that tail is not proportional to the declared grace: internal/agent/exec.go
// runs the child under exec.CommandContext with the default cancel, so the agent
// SIGKILLs it immediately no matter what the DAG asked for, and what is left is
// one outcome-record write and one report RPC.
func TestBuildPodDeadlineCapsTerminationGraceTerm(t *testing.T) {
	headroom := int64(defaultDispatchLostThreshold / time.Second)
	const graceCap = int64(maxDeadlineGraceTerm)
	for _, tc := range []struct {
		name  string
		grace *int64
		want  int64
	}{
		// The overshoot case: an hour of declared grace buys 60 s of deadline.
		{"grace far above the cap", ptr(int64(3600)), 600 + 180 + graceCap},
		{"grace exactly at the cap", ptr(int64(60)), 600 + 180 + 60},
		{"grace below the cap is added verbatim", ptr(int64(45)), 600 + 180 + 45},
		// A declared 0 gets no post-deadline tail from the kubelet at all, so
		// there is nothing to size from the declaration: the default fallback
		// stands, exactly as when nothing is declared.
		{"declared zero falls back to the default", ptr(int64(0)), 600 + 180 + corev1.DefaultTerminationGracePeriodSeconds},
		{"undeclared falls back to the default", nil, 600 + 180 + corev1.DefaultTerminationGracePeriodSeconds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleReq() // TimeoutSeconds: 600
			req.Execution.TerminationGracePeriodSeconds = tc.grace
			got := BuildPod(req).Spec.ActiveDeadlineSeconds
			if got == nil {
				t.Fatal("a declared timeout must still produce a pod deadline")
			}
			if ceiling := int64(req.TimeoutSeconds) + headroom + graceCap; *got > ceiling {
				t.Errorf("activeDeadlineSeconds = %d, must not exceed %d (timeout %d + headroom %d + capped grace %d); "+
					"an unvalidated declared grace must not extend the pod's overshoot without bound",
					*got, ceiling, req.TimeoutSeconds, headroom, graceCap)
			}
			if *got != tc.want {
				t.Errorf("activeDeadlineSeconds = %d, want %d", *got, tc.want)
			}
		})
	}
}
