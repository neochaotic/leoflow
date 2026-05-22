package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

type recordedTransition struct {
	taskID string
	state  domain.TaskState
}

type fakeSink struct {
	mu          sync.Mutex
	transitions []recordedTransition
}

func (s *fakeSink) Transition(_ context.Context, _, taskID string, state domain.TaskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, recordedTransition{taskID, state})
	return nil
}

func (s *fakeSink) last() domain.TaskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transitions) == 0 {
		return ""
	}
	return s.transitions[len(s.transitions)-1].state
}

type fakeMetrics struct {
	transitions int
	durations   int
}

func (m *fakeMetrics) RecordTaskTransition(_, _, _ string)          { m.transitions++ }
func (m *fakeMetrics) RecordTaskDuration(_, _, _ string, _ float64) { m.durations++ }

func httpTask(taskID, url, method string, timeout int) domain.TaskSpec {
	t := domain.TaskSpec{
		TaskID:      taskID,
		Type:        domain.TaskTypeHTTPAPI,
		HTTPRequest: &domain.HTTPRequest{Method: method, URL: url},
	}
	if timeout > 0 {
		t.ExecutionTimeoutSeconds = &timeout
	}
	return t
}

func TestInlineRunnerSuccess(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &fakeSink{}
	metrics := &fakeMetrics{}
	r := NewInlineRunner(sink, metrics, 4, 300, "leoflow/test")

	started, err := r.Start(context.Background(), "run1", "etl", httpTask("t1", srv.URL, http.MethodGet, 0))
	if err != nil || !started {
		t.Fatalf("Start = (%v, %v), want (true, nil)", started, err)
	}
	r.Wait()

	if sink.last() != domain.TaskStateSuccess {
		t.Errorf("terminal state = %q, want success", sink.last())
	}
	if gotUA != "leoflow/test" {
		t.Errorf("User-Agent = %q, want leoflow/test", gotUA)
	}
	if metrics.transitions == 0 || metrics.durations == 0 {
		t.Errorf("metrics not emitted: %+v", metrics)
	}
}

func TestInlineRunnerNon2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := &fakeSink{}
	r := NewInlineRunner(sink, nil, 4, 300, "")
	if _, err := r.Start(context.Background(), "run1", "etl", httpTask("t1", srv.URL, http.MethodGet, 0)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Wait()
	if sink.last() != domain.TaskStateFailed {
		t.Errorf("terminal state = %q, want failed", sink.last())
	}
}

func TestInlineRunnerRecoversPanic(t *testing.T) {
	sink := &fakeSink{}
	r := NewInlineRunner(sink, nil, 4, 300, "")
	r.exec = func(context.Context, Request) error { panic("boom") }
	if _, err := r.Start(context.Background(), "run1", "etl", httpTask("t1", "http://x", http.MethodGet, 0)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Wait()
	if sink.last() != domain.TaskStateFailed {
		t.Errorf("panic should mark the task failed, got %q", sink.last())
	}
}

func TestInlineRunnerSemaphoreExhaustion(t *testing.T) {
	sink := &fakeSink{}
	r := NewInlineRunner(sink, nil, 1, 300, "")
	block := make(chan struct{})
	r.exec = func(context.Context, Request) error {
		<-block
		return nil
	}
	started1, _ := r.Start(context.Background(), "run1", "etl", httpTask("t1", "http://x", http.MethodGet, 0))
	if !started1 {
		t.Fatal("first task should start")
	}
	started2, err2 := r.Start(context.Background(), "run1", "etl", httpTask("t2", "http://x", http.MethodGet, 0))
	if started2 || err2 != nil {
		t.Errorf("second task with full semaphore = (%v, %v), want (false, nil)", started2, err2)
	}
	close(block)
	r.Wait()
}

func TestInlineRunnerRejectsTimeoutAboveCap(t *testing.T) {
	r := NewInlineRunner(&fakeSink{}, nil, 4, 300, "")
	started, err := r.Start(context.Background(), "run1", "etl", httpTask("t1", "http://x", http.MethodGet, 600))
	if started || err == nil {
		t.Errorf("timeout above cap = (%v, %v), want (false, error)", started, err)
	}
}
