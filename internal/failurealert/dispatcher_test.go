package failurealert

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/neochaotic/leoflow/internal/alerts"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
)

type fakeResolver struct {
	urls map[string]string
	err  map[string]error
}

func (f *fakeResolver) ResolveAlertEndpoint(_ context.Context, _, connID string) (string, error) {
	if e := f.err[connID]; e != nil {
		return "", e
	}
	return f.urls[connID], nil
}

type capturedSend struct {
	channelType, url, message string
	ev                        alerts.Event
}

type fakeSender struct {
	mu   sync.Mutex
	sent []capturedSend
	fail map[string]error // keyed by url
}

func (f *fakeSender) Send(_ context.Context, channelType, url, message string, ev alerts.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.fail[url]; e != nil {
		return e
	}
	f.sent = append(f.sent, capturedSend{channelType, url, message, ev})
	return nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func failedRun() scheduler.RunState {
	return scheduler.RunState{
		RunID: "r1", DagID: "etl", TenantID: "t1",
		Tasks: []domain.TaskSpec{{TaskID: "extract"}, {TaskID: "load"}},
		States: map[string]domain.TaskState{
			"extract": domain.TaskStateFailed,
			"load":    domain.TaskStateUpstreamFailed,
		},
		Alerts: &domain.AlertsConfig{OnFailure: []domain.AlertRule{
			{Type: "slack", Conn: "slack_prod", Message: "{{dag}} failed on {{task}}"},
			{Type: "webhook", Conn: "pagerduty"},
		}},
	}
}

// Every rule is resolved, rendered, and sent; the failed task drives the template.
func TestAlertRunFailedSendsEveryRule(t *testing.T) {
	res := &fakeResolver{urls: map[string]string{
		"slack_prod": "https://hooks.slack.com/services/xxx",
		"pagerduty":  "https://events.pagerduty.com/enqueue",
	}}
	snd := &fakeSender{}
	d := New(snd, res, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if len(snd.sent) != 2 {
		t.Fatalf("sent %d alerts, want 2", len(snd.sent))
	}
	got := snd.sent[0]
	if got.channelType != "slack" || got.url != "https://hooks.slack.com/services/xxx" {
		t.Errorf("slack rule mis-sent: %+v", got)
	}
	if got.message != "etl failed on extract" {
		t.Errorf("message = %q, want rendered with the failed task", got.message)
	}
}

// A resolver failure on one rule is logged and skipped; the other rule still sends.
func TestAlertRunFailedIsBestEffort(t *testing.T) {
	res := &fakeResolver{
		urls: map[string]string{"pagerduty": "https://events.pagerduty.com/enqueue"},
		err:  map[string]error{"slack_prod": errors.New("no such connection")},
	}
	snd := &fakeSender{}
	d := New(snd, res, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if len(snd.sent) != 1 || snd.sent[0].channelType != "webhook" {
		t.Fatalf("expected only the webhook to send, got %+v", snd.sent)
	}
}

// The Dispatcher satisfies the scheduler's Alerter seam.
func TestDispatcherImplementsAlerter(t *testing.T) {
	var _ scheduler.Alerter = New(&fakeSender{}, &fakeResolver{}, quietLogger())
}
