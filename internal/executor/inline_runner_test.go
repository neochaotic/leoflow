package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/xcom"
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

type fakeXComPusher struct {
	mu     sync.Mutex
	pushed map[string][]byte
}

func (p *fakeXComPusher) Push(_ context.Context, key xcom.Key, value []byte, _ string, _ map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pushed == nil {
		p.pushed = map[string][]byte{}
	}
	p.pushed[key.String()] = value
	return nil
}

type fakeLogSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *fakeLogSink) Open(logs.Ref) (logs.LogWriter, error) { return &fakeLogWriter{s: s}, nil }
func (s *fakeLogSink) Read(logs.Ref) (io.ReadCloser, error)  { return nil, io.EOF }

type fakeLogWriter struct{ s *fakeLogSink }

func (w *fakeLogWriter) WriteLine(line string) error {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	w.s.lines = append(w.s.lines, line)
	return nil
}
func (w *fakeLogWriter) Close() error { return nil }

func (s *fakeLogSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

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

func newInline(t *testing.T) (*InlineRunner, *fakeSink, *fakeXComPusher, *fakeLogSink) {
	t.Helper()
	sink, px, ls := &fakeSink{}, &fakeXComPusher{}, &fakeLogSink{}
	r := NewInlineRunner(InlineConfig{
		Sink: sink, Metrics: &fakeMetrics{}, XCom: px, Logs: ls,
		Concurrency: 4, MaxSeconds: 300, UserAgent: "leoflow/test",
	})
	return r, sink, px, ls
}

func TestInlineRunnerSuccessShipsXComAndLog(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rows":7}`))
	}))
	defer srv.Close()

	r, sink, px, ls := newInline(t)
	started, err := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t1", srv.URL, http.MethodGet, 0))
	if err != nil || !started {
		t.Fatalf("Start = (%v, %v), want (true, nil)", started, err)
	}
	r.Wait()

	if sink.last() != domain.TaskStateSuccess {
		t.Errorf("terminal state = %q, want success", sink.last())
	}
	if gotUA != "leoflow/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if string(px.pushed["xcom:acme:etl:run1:t1:return_value"]) != `{"rows":7}` {
		t.Errorf("xcom not shipped: %v", px.pushed)
	}
	if ls.count() != 1 {
		t.Errorf("expected one log line, got %d", ls.count())
	}
}

func TestInlineRunnerNon2xxFailsNoXCom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r, sink, px, _ := newInline(t)
	if _, err := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t1", srv.URL, http.MethodGet, 0)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Wait()
	if sink.last() != domain.TaskStateFailed {
		t.Errorf("terminal state = %q, want failed", sink.last())
	}
	if len(px.pushed) != 0 {
		t.Errorf("a failed task must not push xcom, got %v", px.pushed)
	}
}

func TestInlineRunnerRecoversPanic(t *testing.T) {
	r, sink, _, _ := newInline(t)
	r.exec = func(context.Context, Request) ([]byte, error) { panic("boom") }
	if _, err := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t1", "http://x", http.MethodGet, 0)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Wait()
	if sink.last() != domain.TaskStateFailed {
		t.Errorf("panic should mark the task failed, got %q", sink.last())
	}
}

func TestInlineRunnerSemaphoreExhaustion(t *testing.T) {
	r, _, _, _ := newInline(t)
	r.sem = make(chan struct{}, 1)
	block := make(chan struct{})
	r.exec = func(context.Context, Request) ([]byte, error) {
		<-block
		return nil, nil
	}
	started1, _ := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t1", "http://x", http.MethodGet, 0))
	if !started1 {
		t.Fatal("first task should start")
	}
	started2, err2 := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t2", "http://x", http.MethodGet, 0))
	if started2 || err2 != nil {
		t.Errorf("second task at capacity = (%v, %v), want (false, nil)", started2, err2)
	}
	close(block)
	r.Wait()
}

func TestInlineRunnerRejectsTimeoutAboveCap(t *testing.T) {
	r, _, _, _ := newInline(t)
	started, err := r.Start(context.Background(), "run1", "etl", "acme", 1, httpTask("t1", "http://x", http.MethodGet, 600))
	if started || err == nil {
		t.Errorf("timeout above cap = (%v, %v), want (false, error)", started, err)
	}
}
