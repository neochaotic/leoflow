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
