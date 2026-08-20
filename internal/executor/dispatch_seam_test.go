package executor

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestSubprocessExecuteDispositionSuccess pins the Lite success path: a subprocess
// that starts hands the task to the runtime, so Execute reports Dispatched (the
// terminal outcome arrives async over gRPC).
func TestSubprocessExecuteDispositionSuccess(t *testing.T) {
	e := NewSubprocessExecutor(writeScript(t, "true"), discardLogger())
	disp, err := e.Execute(context.Background(), Request{TaskID: "t"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if disp != Dispatched {
		t.Errorf("subprocess start should report Dispatched, got %v", disp)
	}
}

// TestSubprocessExecuteDispositionRejected pins the Lite failure invariant: any
// subprocess dispatch error is permanent (Rejected), never apiserver backpressure
// — preserving today's "every Lite error is permanent" behavior.
func TestSubprocessExecuteDispositionRejected(t *testing.T) {
	e := NewSubprocessExecutor("/no/such/agent-binary", discardLogger())
	disp, err := e.Execute(context.Background(), Request{TaskID: "t"})
	if err == nil {
		t.Fatal("an un-startable agent binary should error synchronously")
	}
	if disp != Rejected {
		t.Errorf("a Lite dispatch error must be Rejected (never Backpressure), got %v", disp)
	}
}

// TestKubernetesExecuteDispositionSuccess pins the pod-path success: a created pod
// is handed to the runtime, so Execute reports Dispatched.
func TestKubernetesExecuteDispositionSuccess(t *testing.T) {
	e := NewKubernetesExecutor(fake.NewClientset(), "leoflow")
	disp, err := e.Execute(context.Background(), sampleReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if disp != Dispatched {
		t.Errorf("a created pod should report Dispatched, got %v", disp)
	}
}

// TestKubernetesExecuteDispositionBackpressure pins that the executor classifies
// its OWN dispatch error: a ResourceQuota 403 on pod CREATE surfaces as
// Backpressure with the cause preserved for the scheduler's note/log.
func TestKubernetesExecuteDispositionBackpressure(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, quota403()
	})
	e := NewKubernetesExecutor(cs, "leoflow")
	disp, err := e.Execute(context.Background(), sampleReq())
	if err == nil {
		t.Fatal("a quota-rejected pod CREATE should error")
	}
	if disp != Backpressure {
		t.Errorf("a ResourceQuota 403 should classify as Backpressure, got %v", disp)
	}
}

// TestKubernetesExecuteDispositionRejected pins the permanent pod-path failure: a
// non-backpressure CREATE error (e.g. RBAC) surfaces as Rejected.
func TestKubernetesExecuteDispositionRejected(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("kube-apiserver unreachable")
	})
	e := NewKubernetesExecutor(cs, "leoflow")
	disp, err := e.Execute(context.Background(), sampleReq())
	if err == nil {
		t.Fatal("a failed pod CREATE should error")
	}
	if disp != Rejected {
		t.Errorf("a non-backpressure CREATE error should classify as Rejected, got %v", disp)
	}
}
