package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neochaotic/leoflow/internal/taskoutcome"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// readOutcome reads and decodes the durable outcome record the agent wrote to its
// termination-log path, failing the test if it is absent or undecodable.
func readOutcome(t *testing.T, path string) taskoutcome.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("termination log not written: %v", err)
	}
	rec, ok := taskoutcome.Decode(string(data))
	if !ok {
		t.Fatalf("termination log is not a decodable outcome record: %q", data)
	}
	return rec
}

// TestRunnerWritesSuccessOutcome: a task that succeeds leaves a `success` record
// in the termination log, so a reconciler can recover the success even if the
// report is lost (ADR 0052). The success record is written only after the
// pre-report pushes, since report(SUCCESS) is the last step of the terminal path.
func TestRunnerWritesSuccessOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"}}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Success {
		t.Errorf("outcome = %q, want success", rec.Outcome)
	}
}

// TestRunnerWritesFailedOutcome: a task that exits non-zero leaves a `failed`
// record carrying the exit code.
func TestRunnerWritesFailedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:boom"}}
	r := newRunner(client, &fakeCmd{exitCode: 1}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a non-zero exit must fail")
	}
	rec := readOutcome(t, path)
	if rec.Outcome != taskoutcome.Failed {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 1 {
		t.Errorf("exit_code = %v, want 1", rec.ExitCode)
	}
}

// TestRunnerWritesFailedOutcomeOnTimeout pins that the execution-timeout sink
// (failWithReason, not fail) also leaves a durable record. The record is keyed off
// the reported state inside report(), so every failure sink is covered — the exact
// gap a "write before each fail()" approach would miss (ADR 0052 review).
func TestRunnerWritesFailedOutcomeOnTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:                "bash",
		Entrypoint:              "sleep 1000",
		ExecutionTimeoutSeconds: 1,
	}}
	r := newRunner(client, &fakeCmd{blockUntilCancel: true}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task exceeding its execution timeout must fail")
	}
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Failed {
		t.Errorf("timeout outcome = %q, want failed", rec.Outcome)
	}
}

// TestRunnerWritesRescheduleOutcome: a reschedule-mode sensor leaves a
// `reschedule` record carrying the next-poke time, so a lost report can still be
// settled as up_for_reschedule with the real time rather than an invented one.
func TestRunnerWritesRescheduleOutcome(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "reschedule.txt")
	if err := os.WriteFile(rp, []byte("2099-01-02T03:04:05+00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:sensor"}}
	r := newRunner(client, &fakeCmd{exitCode: rescheduleExitCode}, &recordingSink{})
	r.ReschedulePath = rp
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: reschedule must not surface as an error: %v", err)
	}
	rec := readOutcome(t, path)
	if rec.Outcome != taskoutcome.Reschedule {
		t.Fatalf("outcome = %q, want reschedule", rec.Outcome)
	}
	when, ok := rec.At()
	if !ok {
		t.Fatal("reschedule record must carry a parseable next-poke time")
	}
	if got := when.UTC().Format("2006-01-02T15:04:05Z"); got != "2099-01-02T03:04:05Z" {
		t.Errorf("reschedule_at = %s, want 2099-01-02T03:04:05Z", got)
	}
}

// TestRunnerWritesOutcomeBeforeReport is the durability property: the record is on
// disk even when the report delivery itself fails. This is the whole point — a pod
// killed mid-report still leaves the truth behind.
func TestRunnerWritesOutcomeBeforeReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{
		spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"},
		// Let the RUNNING report land, then lose the terminal SUCCESS report —
		// exactly the pod-killed-mid-report case the record exists to survive.
		failReportState: agentv1.TaskState_TASK_STATE_SUCCESS,
	}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a failed report must surface as an error")
	}
	// Even though the report never landed, the outcome is durable.
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Success {
		t.Errorf("outcome = %q, want success recorded before the (failed) report", rec.Outcome)
	}
}

// TestRunnerNoTerminationLogWhenPathUnset: Lite (subprocess, no pod, in-process
// report) sets no termination-log path and must be entirely unaffected — no file,
// no error.
func TestRunnerNoTerminationLogWhenPathUnset(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"}}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	// TerminationLogPath left empty.
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run with no termination-log path must be unaffected: %v", err)
	}
}
