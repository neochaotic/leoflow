package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := n.Send(context.Background(), "slack", srv.URL, nil, "boom", Event{DagID: "d", RunID: "r"}); err != nil {
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
	if err := n.Send(context.Background(), "webhook", srv.URL, nil, "sales failed", ev); err != nil {
		t.Fatalf("Send webhook: %v", err)
	}
	if body["dag_id"] != "sales" || body["run_id"] != "run-1" || body["message"] != "sales failed" {
		t.Fatalf("webhook payload = %v, want dag_id/run_id/message set", body)
	}
}

func TestSendAppliesAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	headers := map[string]string{"Authorization": "GenieKey abc123"}
	if err := n.Send(context.Background(), "webhook", srv.URL, headers, "ping", Event{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "GenieKey abc123" {
		t.Fatalf("Authorization header = %q, want it applied", gotAuth)
	}
}

func TestSendNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	if err := n.Send(context.Background(), "slack", srv.URL, nil, "x", Event{}); err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}

func TestSendUnknownChannelIsError(t *testing.T) {
	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	if err := n.Send(context.Background(), "carrier-pigeon", "http://x", nil, "m", Event{}); err == nil {
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

// Placeholders and value() are two halves of one vocabulary. If a name is added
// to the list without a case in value(), Render substitutes an empty string and
// the alert silently loses a field; if it is added to value() without the list,
// template validation rejects a placeholder that actually works. This ties them.
func TestEveryPlaceholderIsSubstituted(t *testing.T) {
	ev := Event{
		DagID:       "etl",
		RunID:       "manual__2026-07-30T12:00:00+00:00",
		LogicalDate: "2026-07-30T00:00:00Z",
		FailedTasks: []string{"extract", "load"},
	}
	for _, name := range Placeholders {
		tmpl := "{{" + name + "}}"
		got := Render(tmpl, ev)
		if got == tmpl {
			t.Errorf("%s was left literal — declared in Placeholders with no case in value()", tmpl)
		}
		if got == "" {
			t.Errorf("%s rendered empty on a fully-populated event", tmpl)
		}
	}
}

// A populated event must never render the absent marker.
func TestAbsentMarkerOnlyForMissingValues(t *testing.T) {
	full := Event{DagID: "etl", RunID: "r1", LogicalDate: "2026-07-30T00:00:00Z", FailedTasks: []string{"x"}}
	if got := Render("{{dag}} {{run_id}} {{logical_date}} {{task}} {{tasks}}", full); strings.Contains(got, "(none)") {
		t.Errorf("a fully-populated event rendered the absent marker: %q", got)
	}
	empty := Event{}
	got := Render("{{dag}}|{{run_id}}|{{logical_date}}|{{task}}|{{tasks}}", empty)
	if got != "(none)|(none)|(none)|(none)|(none)" {
		t.Errorf("empty event rendered %q, want the marker for every field", got)
	}
}
