package executor

import (
	"testing"
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
// make).
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
			pod := BuildPod(req)
			if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != int64(tc.timeout) {
				t.Errorf("activeDeadlineSeconds = %v, want the user's %d", pod.Spec.ActiveDeadlineSeconds, tc.timeout)
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
