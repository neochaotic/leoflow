package observability

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// touchAll exercises every metric once so the collectors produce a metric
// family on Gather (unused label vectors emit nothing otherwise).
func touchAll(m *Metrics) {
	m.SchedulerLoopDuration.Observe(0.1)
	m.SchedulerDecisions.WithLabelValues("schedule").Inc()
	m.SchedulerLeader.WithLabelValues("r1").Set(1)
	m.ActiveDAGRuns.WithLabelValues("etl", "running").Set(1)
	m.QueuedTasks.WithLabelValues("etl").Set(1)
	m.TaskStateTransitions.WithLabelValues("none", "scheduled", "etl").Inc()
	m.TaskDuration.WithLabelValues("etl", "t1", "python").Observe(1)
	m.TaskRetries.WithLabelValues("etl", "t1").Inc()
	m.TaskPodCreationDuration.Observe(1)
	m.TaskColdStart.WithLabelValues("etl").Observe(1)
	m.XComSize.WithLabelValues("etl").Observe(128)
	m.XComPush.WithLabelValues("etl").Inc()
	m.XComPull.WithLabelValues("etl").Inc()
	m.XComRejected.WithLabelValues("too_large").Inc()
	m.HTTPRequests.WithLabelValues("GET", "/api/v2/dags", "200").Inc()
	m.HTTPRequestDuration.WithLabelValues("GET", "/api/v2/dags").Observe(0.01)
	m.AuthFailures.WithLabelValues("bad_password").Inc()
	m.PodsCreated.WithLabelValues("etl", "success").Inc()
	m.PodsRunning.Set(3)
	m.PodPendingDuration.Observe(1)
	m.KubernetesAPICalls.WithLabelValues("create_pod", "success").Inc()
}

func TestRecordTaskDurationObserves(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordTaskTransition("running", "success", "etl")
	m.RecordTaskDuration("etl", "hook", "http_api", 1.5)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != "leoflow_task_duration_seconds" {
			continue
		}
		if n := fam.GetMetric()[0].GetHistogram().GetSampleCount(); n != 1 {
			t.Errorf("task duration sample count = %d, want 1", n)
		}
		return
	}
	t.Error("leoflow_task_duration_seconds not recorded")
}

func TestNewMetricsRegistersAllADR0010Metrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	touchAll(m)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	got := make(map[string]bool, len(families))
	for _, fam := range families {
		got[fam.GetName()] = true
	}

	want := []string{
		"leoflow_scheduler_loop_duration_seconds",
		"leoflow_scheduler_decisions_total",
		"leoflow_scheduler_leader",
		"leoflow_active_dag_runs",
		"leoflow_queued_tasks",
		"leoflow_task_state_transitions_total",
		"leoflow_task_duration_seconds",
		"leoflow_task_retries_total",
		"leoflow_task_pod_creation_duration_seconds",
		"leoflow_task_cold_start_seconds",
		"leoflow_xcom_size_bytes",
		"leoflow_xcom_push_total",
		"leoflow_xcom_pull_total",
		"leoflow_xcom_rejected_total",
		"leoflow_http_requests_total",
		"leoflow_http_request_duration_seconds",
		"leoflow_auth_failures_total",
		"leoflow_pods_created_total",
		"leoflow_pods_running",
		"leoflow_pod_pending_duration_seconds",
		"leoflow_kubernetes_api_calls_total",
	}
	var missing []string
	for _, name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("missing metrics: %v", missing)
	}
}
