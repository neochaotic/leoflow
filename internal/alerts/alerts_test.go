package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRenderSubstitutesRunContext(t *testing.T) {
	ev := Event{DagID: "sales", RunID: "run-1", LogicalDate: "2026-07-17", FailedTasks: []string{"extract", "load"}}
	got := Render("{{dag}} {{run_id}} @ {{logical_date}} failed: {{tasks}}", ev)
	want := "sales run-1 @ 2026-07-17 failed: extract, load"
	if got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// An empty template yields a sensible default summary rather than a blank message.
func TestRenderEmptyTemplateDefaults(t *testing.T) {
	ev := Event{DagID: "sales", RunID: "run-1", FailedTasks: []string{"load"}}
	got := Render("", ev)
	if got == "" {
		t.Fatal("empty template must produce a default summary")
	}
	if !contains(got, "sales") || !contains(got, "run-1") {
		t.Fatalf("default summary %q missing dag/run", got)
	}
}

func TestSendSlackPostsText(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	if err := n.Send(context.Background(), "slack", srv.URL, "boom", Event{DagID: "d", RunID: "r"}); err != nil {
		t.Fatalf("Send slack: %v", err)
	}
	if body["text"] != "boom" {
		t.Fatalf("slack payload = %v, want text=boom", body)
	}
}

func TestSendWebhookPostsStructuredPayload(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	ev := Event{DagID: "sales", RunID: "run-1", FailedTasks: []string{"load"}}
	if err := n.Send(context.Background(), "webhook", srv.URL, "sales failed", ev); err != nil {
		t.Fatalf("Send webhook: %v", err)
	}
	if body["dag_id"] != "sales" || body["run_id"] != "run-1" || body["message"] != "sales failed" {
		t.Fatalf("webhook payload = %v, want dag_id/run_id/message set", body)
	}
}

func TestSendNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	if err := n.Send(context.Background(), "slack", srv.URL, "x", Event{}); err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}

func TestSendUnknownChannelIsError(t *testing.T) {
	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	if err := n.Send(context.Background(), "carrier-pigeon", "http://x", "m", Event{}); err == nil {
		t.Fatal("expected error on unknown channel type")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
