package observability

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds every Prometheus collector required by ADR 0010. Construct it
// once with NewMetrics and pass it to the components that emit values.
type Metrics struct {
	// Scheduler
	SchedulerLoopDuration prometheus.Histogram
	SchedulerDecisions    *prometheus.CounterVec
	SchedulerLeader       *prometheus.GaugeVec
	ActiveDAGRuns         *prometheus.GaugeVec
	QueuedTasks           *prometheus.GaugeVec
	TasksUndispatchable   *prometheus.CounterVec

	// Task lifecycle
	TaskStateTransitions    *prometheus.CounterVec
	TaskDuration            *prometheus.HistogramVec
	TaskRetries             *prometheus.CounterVec
	TaskPodCreationDuration prometheus.Histogram
	TaskColdStart           *prometheus.HistogramVec

	// XCom
	XComSize     *prometheus.HistogramVec
	XComPush     *prometheus.CounterVec
	XComPull     *prometheus.CounterVec
	XComRejected *prometheus.CounterVec

	// API
	HTTPRequests        *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	AuthFailures        *prometheus.CounterVec

	// Executor (Kubernetes)
	PodsCreated        *prometheus.CounterVec
	PodsRunning        prometheus.Gauge
	PodPendingDuration prometheus.Histogram
	KubernetesAPICalls *prometheus.CounterVec
}

// NewMetrics registers every ADR 0010 collector with reg and returns the set.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	sizeBuckets := prometheus.ExponentialBuckets(64, 4, 8)
	return &Metrics{
		SchedulerLoopDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "leoflow_scheduler_loop_duration_seconds", Help: "Duration of one scheduler loop iteration.",
		}),
		SchedulerDecisions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_scheduler_decisions_total", Help: "Scheduler decisions by type.",
		}, []string{"decision_type"}),
		TasksUndispatchable: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_tasks_undispatchable_total", Help: "Tasks queued with no executor to launch them, by reason.",
		}, []string{"reason"}),
		SchedulerLeader: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "leoflow_scheduler_leader", Help: "1 when this replica is the scheduler leader.",
		}, []string{"replica_id"}),
		ActiveDAGRuns: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "leoflow_active_dag_runs", Help: "Active dag runs by dag and state.",
		}, []string{"dag_id", "state"}),
		QueuedTasks: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "leoflow_queued_tasks", Help: "Queued task instances by dag.",
		}, []string{"dag_id"}),

		TaskStateTransitions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_task_state_transitions_total", Help: "Task state transitions.",
		}, []string{"from_state", "to_state", "dag_id"}),
		TaskDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leoflow_task_duration_seconds", Help: "Task execution duration.",
		}, []string{"dag_id", "task_id", "task_type"}),
		TaskRetries: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_task_retries_total", Help: "Task retries.",
		}, []string{"dag_id", "task_id"}),
		TaskPodCreationDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "leoflow_task_pod_creation_duration_seconds", Help: "Time to create a task pod.",
		}),
		TaskColdStart: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leoflow_task_cold_start_seconds", Help: "Task cold start time.",
		}, []string{"dag_id"}),

		XComSize: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leoflow_xcom_size_bytes", Help: "XCom payload size in bytes.", Buckets: sizeBuckets,
		}, []string{"dag_id"}),
		XComPush: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_xcom_push_total", Help: "XCom pushes.",
		}, []string{"dag_id"}),
		XComPull: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_xcom_pull_total", Help: "XCom pulls.",
		}, []string{"dag_id"}),
		XComRejected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_xcom_rejected_total", Help: "Rejected XCom writes by reason.",
		}, []string{"reason"}),

		HTTPRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_http_requests_total", Help: "HTTP requests.",
		}, []string{"method", "path", "status"}),
		HTTPRequestDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "leoflow_http_request_duration_seconds", Help: "HTTP request duration.",
		}, []string{"method", "path"}),
		AuthFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_auth_failures_total", Help: "Authentication failures by reason.",
		}, []string{"reason"}),

		PodsCreated: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_pods_created_total", Help: "Pods created by dag and result.",
		}, []string{"dag_id", "result"}),
		PodsRunning: f.NewGauge(prometheus.GaugeOpts{
			Name: "leoflow_pods_running", Help: "Currently running pods.",
		}),
		PodPendingDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "leoflow_pod_pending_duration_seconds", Help: "Pod pending duration.",
		}),
		KubernetesAPICalls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "leoflow_kubernetes_api_calls_total", Help: "Kubernetes API calls.",
		}, []string{"operation", "result"}),
	}
}

// RecordHTTPRequest records a completed HTTP request (count + duration).
func (m *Metrics) RecordHTTPRequest(method, path string, status int, dur time.Duration) {
	m.HTTPRequests.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, path).Observe(dur.Seconds())
}

// RecordSchedulerDecision records one scheduler decision by type.
func (m *Metrics) RecordSchedulerDecision(decisionType string) {
	m.SchedulerDecisions.WithLabelValues(decisionType).Inc()
}

// RecordTaskTransition records a task instance state transition.
func (m *Metrics) RecordTaskTransition(from, to, dagID string) {
	m.TaskStateTransitions.WithLabelValues(from, to, dagID).Inc()
}

// RecordUndispatchable records a task that became queued but has no executor to
// launch it (e.g. pod dispatch disabled), so an operator can distinguish a
// resource/config gap from an actual bug.
func (m *Metrics) RecordUndispatchable(reason string) {
	m.TasksUndispatchable.WithLabelValues(reason).Inc()
}

// RecordTaskDuration records how long a task took to execute, in seconds.
func (m *Metrics) RecordTaskDuration(dagID, taskID, taskType string, seconds float64) {
	m.TaskDuration.WithLabelValues(dagID, taskID, taskType).Observe(seconds)
}
