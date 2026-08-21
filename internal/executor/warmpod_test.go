package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// warmEnvMap projects the warm pod's single container env into a name->value map,
// for value assertions. Env vars sourced from elsewhere (SecretKeyRef) have an
// empty value here.
func warmEnvMap(pod *corev1.Pod) map[string]string {
	m := map[string]string{}
	for _, e := range pod.Spec.Containers[0].Env {
		m[e.Name] = e.Value
	}
	return m
}

// warmEnvVar returns the full EnvVar named name from the warm pod's container, so
// a test can inspect ValueFrom (downward-API sources carry no literal Value).
func warmEnvVar(pod *corev1.Pod, name string) (corev1.EnvVar, bool) {
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

// TestBuildWarmPodInjectsPodNameViaDownwardAPI locks the durable-binding key
// plumbing (ADR 0058 N1d-a1): the worker learns its OWN pod name through the
// Kubernetes downward API as LEOFLOW_POD_NAME (fieldRef metadata.name), since the
// name — carrying a random suffix — is unknown at build time. The agent forwards
// it in WorkerRegister.pod_name so the control plane can bind a started attempt
// to it as warm_worker_id.
func TestBuildWarmPodInjectsPodNameViaDownwardAPI(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())

	ev, ok := warmEnvVar(pod, "LEOFLOW_POD_NAME")
	if !ok {
		t.Fatal("warm pod must carry LEOFLOW_POD_NAME env for the durable binding key")
	}
	if ev.Value != "" {
		t.Errorf("LEOFLOW_POD_NAME must be sourced from the downward API, not a literal value (got %q)", ev.Value)
	}
	if ev.ValueFrom == nil || ev.ValueFrom.FieldRef == nil {
		t.Fatalf("LEOFLOW_POD_NAME must use valueFrom.fieldRef, got %+v", ev.ValueFrom)
	}
	if got := ev.ValueFrom.FieldRef.FieldPath; got != "metadata.name" {
		t.Errorf("LEOFLOW_POD_NAME fieldRef.fieldPath = %q, want metadata.name", got)
	}
}

// baseWarmSpec is a minimal env-var-transport warm spec used across the cases.
func baseWarmSpec() WarmPodSpec {
	return WarmPodSpec{
		DagVersionID:     "dv-abc",
		Image:            "reg.example/dag:v3",
		Namespace:        "leoflow",
		ControlPlaneAddr: "cp:9000",
		BootstrapToken:   "boot-tok",
	}
}

// TestBuildWarmPodWarmEnvAndLabels locks the warm-worker pod shape (ADR 0058
// N1b2b): the agent is told to run in warm mode for a dag_version, the pod is
// labeled so the reconciler can list/count it, and it carries NO task-specific
// env (a warm worker has no task instance until an attempt arrives in-band).
func TestBuildWarmPodWarmEnvAndLabels(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())

	env := warmEnvMap(pod)
	if env["LEOFLOW_WARM_WORKER"] != "1" {
		t.Errorf("LEOFLOW_WARM_WORKER = %q, want 1", env["LEOFLOW_WARM_WORKER"])
	}
	if env["LEOFLOW_DAG_VERSION_ID"] != "dv-abc" {
		t.Errorf("LEOFLOW_DAG_VERSION_ID = %q, want dv-abc", env["LEOFLOW_DAG_VERSION_ID"])
	}
	if env["LEOFLOW_CONTROL_PLANE_ADDR"] != "cp:9000" {
		t.Errorf("LEOFLOW_CONTROL_PLANE_ADDR = %q, want cp:9000", env["LEOFLOW_CONTROL_PLANE_ADDR"])
	}

	// No task-specific env: the warm worker has no task instance, try number, or
	// per-attempt outcome path — those arrive in-band per attempt (AwaitAssignment).
	for _, k := range []string{"LEOFLOW_TASK_INSTANCE_ID", "LEOFLOW_TERMINATION_LOG_PATH"} {
		if _, ok := env[k]; ok {
			t.Errorf("warm pod must not carry task env %s (got %q)", k, env[k])
		}
	}

	// Image and warm-worker labels the reconciler selects on.
	if got := pod.Spec.Containers[0].Image; got != "reg.example/dag:v3" {
		t.Errorf("image = %q, want reg.example/dag:v3", got)
	}
	if pod.Labels[warmWorkerLabelKey] != "true" {
		t.Errorf("label %s = %q, want true", warmWorkerLabelKey, pod.Labels[warmWorkerLabelKey])
	}
	if pod.Labels[warmDagVersionLabelKey] != "dv-abc" {
		t.Errorf("label %s = %q, want dv-abc", warmDagVersionLabelKey, pod.Labels[warmDagVersionLabelKey])
	}
	// No per-task identity labels leak onto a warm pod.
	for _, k := range []string{"leoflow.io/task-id", "leoflow.io/run-id", "leoflow.io/try-number"} {
		if _, ok := pod.Labels[k]; ok {
			t.Errorf("warm pod must not carry task label %s", k)
		}
	}

	// A warm worker that exits on drain must be REPLACED by the reconciler, not
	// restarted in place (a fresh pod re-registers cleanly); RestartPolicy Never.
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", pod.Spec.RestartPolicy)
	}
	// The agent authenticates over gRPC and never calls the Kubernetes API.
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken must be explicitly false")
	}
}

// TestBuildWarmPodCarriesSelfLifecycleCaps locks the four self-lifecycle caps the
// warm agent enforces on itself (ADR 0058 D9/D10/D6/H3): the attempt count cap, the
// wall-clock lifetime cap (seconds), the idle-TTL (seconds), and the per-attempt
// watchdog (seconds). BuildWarmPod injects them as env so the worker can drain,
// idle-recycle, and hard-bound a wedged attempt without any control-plane round trip.
func TestBuildWarmPodCarriesSelfLifecycleCaps(t *testing.T) {
	spec := baseWarmSpec()
	spec.MaxAttemptsPerWorker = 50
	spec.MaxWorkerLifetimeSeconds = 3600
	spec.WorkerIdleTTLSeconds = 300
	spec.AttemptWatchdogSeconds = 86400

	env := warmEnvMap(BuildWarmPod(spec))
	for _, tc := range []struct{ key, want string }{
		{"LEOFLOW_MAX_ATTEMPTS_PER_WORKER", "50"},
		{"LEOFLOW_MAX_WORKER_LIFETIME_SECONDS", "3600"},
		{"LEOFLOW_WORKER_IDLE_TTL_SECONDS", "300"},
		{"LEOFLOW_ATTEMPT_WATCHDOG_SECONDS", "86400"},
	} {
		if env[tc.key] != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}
}

// TestBuildWarmPodEnvVarTransport locks the default (env-var) bootstrap-token
// transport: the minted worker JWT rides as the plaintext LEOFLOW_AGENT_TOKEN,
// exactly how a task pod carries its token on the env-var path.
func TestBuildWarmPodEnvVarTransport(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())
	env := warmEnvMap(pod)
	if env["LEOFLOW_AGENT_TOKEN"] != "boot-tok" {
		t.Errorf("LEOFLOW_AGENT_TOKEN = %q, want boot-tok", env["LEOFLOW_AGENT_TOKEN"])
	}
	if _, ok := env["LEOFLOW_AGENT_TOKEN_PATH"]; ok {
		t.Error("env-var transport must not set LEOFLOW_AGENT_TOKEN_PATH")
	}
	if _, ok := pod.Annotations[AgentIdentityAnnotation]; ok {
		t.Error("env-var transport must not stamp the exchange identity annotation")
	}
}

// TestBuildWarmPodExchangeTransport locks the exchange bootstrap transport reuse:
// the plaintext token is kept OFF the pod, a ServiceAccount token is projected and
// pointed at by LEOFLOW_AGENT_TOKEN_PATH, and — crucially — NO task-instance
// identity annotation is stamped (a warm worker registers under its bearer's
// identity, not a task instance).
func TestBuildWarmPodExchangeTransport(t *testing.T) {
	spec := baseWarmSpec()
	spec.AgentTokenTransport = agentTransportExchange
	pod := BuildWarmPod(spec)

	env := warmEnvMap(pod)
	if _, ok := env["LEOFLOW_AGENT_TOKEN"]; ok {
		t.Error("exchange: plaintext LEOFLOW_AGENT_TOKEN must not be on the warm pod")
	}
	if env["LEOFLOW_AGENT_TOKEN_TRANSPORT"] != agentTransportExchange {
		t.Errorf("LEOFLOW_AGENT_TOKEN_TRANSPORT = %q, want exchange", env["LEOFLOW_AGENT_TOKEN_TRANSPORT"])
	}
	if env["LEOFLOW_AGENT_TOKEN_PATH"] != agentTokenMountDir+"/"+agentTokenFile {
		t.Errorf("LEOFLOW_AGENT_TOKEN_PATH = %q, want %q", env["LEOFLOW_AGENT_TOKEN_PATH"], agentTokenMountDir+"/"+agentTokenFile)
	}
	if _, ok := pod.Annotations[AgentIdentityAnnotation]; ok {
		t.Error("exchange: a warm worker must NOT carry the task-instance identity annotation")
	}
	// The projected token volume is mounted.
	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == agentTokenVolumeName {
			found = true
		}
	}
	if !found {
		t.Error("exchange: projected agent-token volume must be mounted")
	}
}

// TestBuildWarmPodMountsCA locks CA-mount reuse: when a CA ConfigMap is set the
// warm pod mounts it and selects TLS via the same env the task pod uses.
func TestBuildWarmPodMountsCA(t *testing.T) {
	spec := baseWarmSpec()
	spec.AgentTLSCAConfigMap = "leoflow-ca"
	pod := BuildWarmPod(spec)

	env := warmEnvMap(pod)
	if env["LEOFLOW_AGENT_INSECURE"] != "false" {
		t.Errorf("LEOFLOW_AGENT_INSECURE = %q, want false", env["LEOFLOW_AGENT_INSECURE"])
	}
	if env["LEOFLOW_AGENT_TLS_CA"] != agentCADir+"/"+agentCAFile {
		t.Errorf("LEOFLOW_AGENT_TLS_CA = %q, want %q", env["LEOFLOW_AGENT_TLS_CA"], agentCADir+"/"+agentCAFile)
	}
	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == agentCAVolumeName {
			found = true
		}
	}
	if !found {
		t.Error("CA ConfigMap volume must be mounted")
	}
}
