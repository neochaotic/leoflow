package agent

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
)

// instantRunner builds a Runner whose report retry sleeps are instant, so the
// backoff logic is exercised without real wall-clock delay.
func instantRunner(client *fakeClient) *Runner {
	return &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		afterFunc: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
}

// TestReportRetriesTransientThenSucceeds: a terminal report that hits a
// transient gRPC error (api momentarily Unavailable) is retried with backoff
// and eventually lands — the task result is not lost to a network blip, and no
// reaper has to fail a task that actually succeeded.
func TestReportRetriesTransientThenSucceeds(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: 2}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err != nil {
		t.Fatalf("report should succeed after transient failures, got %v", err)
	}
	if client.reportAttempts != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success), got %d", client.reportAttempts)
	}
	if len(client.reports) != 1 || client.reports[0].GetState() != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("exactly one successful SUCCESS report expected, got %+v", client.reports)
	}
}

// TestReportGivesUpAfterMaxAttempts: a report that never recovers stops after
// the bounded number of attempts and returns an error — it must not loop
// forever holding the task.
func TestReportGivesUpAfterMaxAttempts(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: 1000}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("report should fail after exhausting attempts")
	}
	// One initial attempt plus maxRetryAttempts retries (Backoff yields a delay
	// for attempts 1..maxRetryAttempts, then exhausts on the next), bounding the
	// total at maxRetryAttempts+1 calls / ~31s of backoff.
	if want := maxRetryAttempts + 1; client.reportAttempts != want {
		t.Errorf("expected exactly %d attempts, got %d", want, client.reportAttempts)
	}
}

// TestReportDoesNotRetryNonRetryable: a logical rejection (e.g. InvalidArgument)
// is returned immediately — retrying it would never apply and would only delay
// the failure.
func TestReportDoesNotRetryNonRetryable(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.InvalidArgument, reportFailTimes: 1000}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("a non-retryable report error must be returned")
	}
	if client.reportAttempts != 1 {
		t.Errorf("a non-retryable error must not be retried, got %d attempts", client.reportAttempts)
	}
}

// TestReportRetryAbortsOnContextCancel: while backing off, a canceled context
// (parent shutdown / SIGTERM) aborts the retry promptly rather than sleeping
// out the whole backoff schedule.
func TestReportRetryAbortsOnContextCancel(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: 1000}
	r := &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		// Never fires, so the retry can only leave via ctx cancellation.
		afterFunc: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("report must return the context error when canceled mid-backoff")
	}
	if client.reportAttempts != 1 {
		t.Errorf("expected 1 attempt before the canceled backoff aborted, got %d", client.reportAttempts)
	}
}
