package executor

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/neochaotic/leoflow/internal/domain"
)

func sampleReq() Request {
	return Request{
		TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "r1", TaskID: "extract",
		TryNumber: 1, Image: "img:v1", ImagePullPolicy: "Always", Operator: "python",
		Resources:        domain.Resources{Requests: &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"}},
		Execution:        domain.Execution{NodeSelector: map[string]string{"disktype": "ssd"}, ServiceAccount: "sa"},
		TimeoutSeconds:   600,
		ControlPlaneAddr: "cp:9000",
		AgentToken:       "tok",
	}
}

func TestBuildPod(t *testing.T) {
	pod := BuildPod(sampleReq())

	if !strings.HasPrefix(pod.Name, "leoflow-etl-extract-1-") {
		t.Errorf("pod name = %q, want leoflow-etl-extract-1-* prefix", pod.Name)
	}
	if pod.Labels["leoflow.io/dag-id"] != "etl" || pod.Labels["leoflow.io/try-number"] != "1" {
		t.Errorf("labels = %v", pod.Labels)
	}
	if pod.Annotations["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("annotation = %v", pod.Annotations)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %v, want Never", pod.Spec.RestartPolicy)
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("activeDeadlineSeconds = %v, want 600", pod.Spec.ActiveDeadlineSeconds)
	}
	if pod.Spec.NodeSelector["disktype"] != "ssd" || pod.Spec.ServiceAccountName != "sa" {
		t.Errorf("placement: nodeSelector=%v sa=%q", pod.Spec.NodeSelector, pod.Spec.ServiceAccountName)
	}
	c := pod.Spec.Containers[0]
	if c.Image != "img:v1" || c.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("container image=%q pull=%v", c.Image, c.ImagePullPolicy)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_CONTROL_PLANE_ADDR"] != "cp:9000" || env["LEOFLOW_AGENT_TOKEN"] != "tok" || env["LEOFLOW_TASK_INSTANCE_ID"] != "ti-1" {
		t.Errorf("agent env not injected: %v", env)
	}
	if c.Resources.Requests.Cpu().String() != "500m" || c.Resources.Requests.Memory().String() != "512Mi" {
		t.Errorf("resources = %v", c.Resources.Requests)
	}
}

func TestBuildPodMountsStagingVolume(t *testing.T) {
	// Without a staging claim, no extra volume is added.
	if vols := BuildPod(sampleReq()).Spec.Volumes; len(vols) != 0 {
		t.Errorf("no staging claim should add no volumes, got %v", vols)
	}
	// With a claim, the run's PVC is mounted at /staging and exposed via env.
	req := sampleReq()
	req.StagingClaim = "leoflow-staging-etl-r1"
	pod := BuildPod(req)
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].PersistentVolumeClaim == nil ||
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "leoflow-staging-etl-r1" {
		t.Fatalf("staging volume not wired to the PVC: %+v", pod.Spec.Volumes)
	}
	c := pod.Spec.Containers[0]
	mounted := false
	for _, m := range c.VolumeMounts {
		if m.MountPath == stagingMountPath && m.Name == pod.Spec.Volumes[0].Name {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("staging volume not mounted at %s: %+v", stagingMountPath, c.VolumeMounts)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_STAGING_DIR"] != stagingMountPath {
		t.Errorf("LEOFLOW_STAGING_DIR = %q, want %s", env["LEOFLOW_STAGING_DIR"], stagingMountPath)
	}
}

func TestBuildPodMountsAgentTLSCA(t *testing.T) {
	// No CA configmap -> agent stays insecure (no TLS env, no extra volume).
	base := BuildPod(sampleReq())
	baseEnv := map[string]string{}
	for _, e := range base.Spec.Containers[0].Env {
		baseEnv[e.Name] = e.Value
	}
	if _, ok := baseEnv["LEOFLOW_AGENT_TLS_CA"]; ok {
		t.Error("no CA configmap should not set LEOFLOW_AGENT_TLS_CA")
	}
	// With a CA configmap, mount it and tell the agent to use TLS.
	req := sampleReq()
	req.AgentTLSCAConfigMap = "leoflow-agent-ca"
	pod := BuildPod(req)
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].ConfigMap != nil && pod.Spec.Volumes[i].ConfigMap.Name == "leoflow-agent-ca" {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil {
		t.Fatalf("CA configmap not mounted as a volume: %+v", pod.Spec.Volumes)
	}
	c := pod.Spec.Containers[0]
	mounted := false
	for _, m := range c.VolumeMounts {
		if m.Name == vol.Name {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("CA volume not mounted: %+v", c.VolumeMounts)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_AGENT_INSECURE"] != "false" {
		t.Errorf("LEOFLOW_AGENT_INSECURE = %q, want false", env["LEOFLOW_AGENT_INSECURE"])
	}
	if env["LEOFLOW_AGENT_TLS_CA"] == "" {
		t.Error("LEOFLOW_AGENT_TLS_CA must point to the mounted CA")
	}
}

func TestBuildPodSanitizesName(t *testing.T) {
	req := sampleReq()
	req.DagID = "ETL Vendas"
	req.TaskID = "Extract_Data"
	if name := BuildPod(req).Name; !strings.HasPrefix(name, "leoflow-etl-vendas-extract-data-1-") {
		t.Errorf("sanitized name = %q", name)
	}
}

func TestKubernetesExecutorCreatesPod(t *testing.T) {
	cs := fake.NewClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.Execute(context.Background(), sampleReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	pods, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("want 1 pod created, got %d", len(pods.Items))
	}
}

func TestKubernetesExecutorRejectsHTTPAPIPod(t *testing.T) {
	e := NewKubernetesExecutor(fake.NewClientset(), "leoflow")
	req := sampleReq()
	req.Operator = "http_api"
	err := e.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("pod-mode http_api should not be implemented yet")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %q, want a 'not yet implemented' message", err)
	}
}
