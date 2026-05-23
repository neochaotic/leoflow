package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestTimetableDescription(t *testing.T) {
	cases := map[string]string{
		"@daily":      "Daily",
		"@hourly":     "Hourly",
		"@weekly":     "Weekly",
		"*/5 * * * *": "Every 5 minutes",
		"0 */2 * * *": "Every 2 hours",
		"30 6 * * *":  "At 06:30, every day",
		"0 0 1 * *":   "0 0 1 * *", // unrecognized -> raw fallback
		"weird-cron":  "weird-cron",
	}
	for in, want := range cases {
		got := timetableDescription(&in)
		if got == nil || *got != want {
			t.Errorf("timetableDescription(%q) = %v, want %q", in, deref(got), want)
		}
	}
	if timetableDescription(nil) != nil {
		t.Errorf("nil schedule should yield null description")
	}
	empty := ""
	if timetableDescription(&empty) != nil {
		t.Errorf("empty schedule should yield null description")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestDagDetailsFullShape(t *testing.T) {
	sched := "0 5 * * *"
	srv := uiDagsServer([]domain.DAG{{
		DagID: "etl", Owner: "data", Tags: []string{"x"}, Schedule: &sched,
		IsActive: true, Catchup: true, MaxActiveRuns: 8, Description: "the etl",
	}}, &fakeLatestRuns{})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"dag_id", "dag_display_name", "is_paused", "is_stale", "last_parsed_time",
		"last_parse_duration", "last_expired", "bundle_name", "bundle_version",
		"relative_fileloc", "fileloc", "description", "timetable_summary",
		"timetable_description", "timetable_partitioned", "tags", "max_active_tasks",
		"max_active_runs", "max_consecutive_failed_dag_runs", "has_task_concurrency_limits",
		"has_import_errors", "next_dagrun_logical_date", "next_dagrun_data_interval_start",
		"next_dagrun_data_interval_end", "next_dagrun_run_after", "allowed_run_types",
		"owners", "catchup", "dag_run_timeout", "asset_expression", "doc_md", "start_date",
		"end_date", "is_paused_upon_creation", "params", "render_template_as_native_obj",
		"template_search_path", "timezone", "last_parsed", "default_args", "file_token",
		"concurrency", "latest_dag_version",
	}
	for _, f := range required {
		if _, ok := d[f]; !ok {
			t.Errorf("details missing required field %q", f)
		}
	}
	var desc string
	_ = json.Unmarshal(d["timetable_description"], &desc)
	if desc != "At 05:00, every day" {
		t.Errorf("timetable_description = %q", desc)
	}
}
