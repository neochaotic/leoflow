package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/executor"
)

// TestPlatformDefaultsEmitLimits locks the QoS half of #725: when the cluster
// configures executor.defaults.resources_*, the L0 default must carry BOTH
// Requests and Limits. Requests alone yields Burstable (or, when the key never
// bound, BestEffort) QoS; only Requests == Limits reaches Guaranteed, which is
// the whole point of pinning a per-cluster default.
func TestPlatformDefaultsEmitLimits(t *testing.T) {
	pd := platformDefaults(config.PlatformDefaultsSection{
		ResourcesCPU:    "250m",
		ResourcesMemory: "256Mi",
	})
	if pd.Resources == nil {
		t.Fatal("platform default Resources is nil despite configured cpu/memory")
	}
	if pd.Resources.Requests == nil {
		t.Fatal("platform default Requests is nil")
	}
	if pd.Resources.Limits == nil {
		t.Fatal("platform default Limits is nil: task pods relying on the default " +
			"cannot reach Guaranteed QoS")
	}
	if pd.Resources.Limits.CPU != "250m" || pd.Resources.Limits.Memory != "256Mi" {
		t.Errorf("Limits = %+v, want cpu=250m memory=256Mi", *pd.Resources.Limits)
	}
	if pd.Resources.Requests.CPU != "250m" || pd.Resources.Requests.Memory != "256Mi" {
		t.Errorf("Requests = %+v, want cpu=250m memory=256Mi", *pd.Resources.Requests)
	}
}

// TestDefaultResourcesBuildGuaranteedPod chains the real mapping to the executor:
// a task that relies on the platform default (no resources of its own) must build
// a pod whose container carries both requests and limits, i.e. Guaranteed QoS.
func TestDefaultResourcesBuildGuaranteedPod(t *testing.T) {
	pd := platformDefaults(config.PlatformDefaultsSection{
		ResourcesCPU:    "250m",
		ResourcesMemory: "256Mi",
	})
	if pd.Resources == nil {
		t.Fatal("platform default Resources is nil despite configured cpu/memory")
	}
	// The dispatcher assigns the platform default verbatim to a task that declared
	// no resources of its own (see internal/dispatch: req.Resources = *d.defaults.Resources).
	req := executor.Request{
		TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "r1",
		TaskID: "extract", TryNumber: 1, Image: "img:v1", Operator: "python",
		Resources: *pd.Resources,
	}
	pod := executor.BuildPod(req)
	c := pod.Spec.Containers[0]
	if len(c.Resources.Requests) == 0 {
		t.Fatal("pod container has no resource requests")
	}
	if len(c.Resources.Limits) == 0 {
		t.Fatal("pod container has no resource limits: BestEffort/Burstable, not Guaranteed")
	}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		req := c.Resources.Requests[name]
		lim := c.Resources.Limits[name]
		if req.IsZero() || lim.IsZero() || req.Cmp(lim) != 0 {
			t.Errorf("%s: request=%s limit=%s, want equal and non-zero (Guaranteed)",
				name, req.String(), lim.String())
		}
	}
}
