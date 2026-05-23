package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeLogReader struct {
	body    string
	err     error
	tailed  []string
	tailErr error
}

func (f *fakeLogReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func (f *fakeLogReader) Tail(context.Context, string, string, string, string, int) (lines <-chan string, cancel func(), err error) {
	if f.tailErr != nil {
		return nil, nil, f.tailErr
	}
	ch := make(chan string, len(f.tailed))
	for _, l := range f.tailed {
		ch <- l
	}
	close(ch)
	return ch, func() {}, nil
}

func logsServer(reader LogReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         &fakeTaskRepo{},
		Logs:          reader,
	})
}

func TestLogsReadStreamsFile(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{body: "line one\nline two\n"}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs read = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "line one\nline two\n" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestLogsStructuredJSON(t *testing.T) {
	body := "DAG bundles loaded\nERROR something broke\nWARN heads up\n"
	srv := logsServer(&fakeLogReader{body: body})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("structured logs = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want json", ct)
	}
	var got struct {
		Content []struct {
			Event   string   `json:"event"`
			Level   string   `json:"level"`
			Sources []string `json:"sources"`
		} `json:"content"`
		ContinuationToken *string `json:"continuation_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Leading collapsible source group (the drill-down fold) + endgroup.
	if len(got.Content) < 5 {
		t.Fatalf("want group + endgroup + 3 lines, got %d items: %+v", len(got.Content), got.Content)
	}
	if !strings.HasPrefix(got.Content[0].Event, "::group::") || len(got.Content[0].Sources) == 0 {
		t.Errorf("first item must be a source group, got %+v", got.Content[0])
	}
	if got.Content[1].Event != "::endgroup::" {
		t.Errorf("second item must close the group, got %q", got.Content[1].Event)
	}
	// Per-line level drives the UI coloring.
	levels := map[string]string{}
	for _, e := range got.Content[2:] {
		levels[e.Event] = e.Level
	}
	if levels["ERROR something broke"] != "error" {
		t.Errorf("error line level = %q", levels["ERROR something broke"])
	}
	if levels["WARN heads up"] != "warning" {
		t.Errorf("warn line level = %q", levels["WARN heads up"])
	}
	if levels["DAG bundles loaded"] != "info" {
		t.Errorf("plain line level = %q, want info", levels["DAG bundles loaded"])
	}
	if got.ContinuationToken != nil {
		t.Errorf("continuation_token should be null, got %v", *got.ContinuationToken)
	}
}

func TestLogsReadNotFound(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{err: domain.ErrNotFound}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing logs = %d, want 404", rec.Code)
	}
}

func TestLogsReadFollowStreamsTailedLines(t *testing.T) {
	reader := &fakeLogReader{body: "stored\n", tailed: []string{"live one", "live two"}}
	rec := authGet(logsServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("follow read = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "stored\nlive one\nlive two\n" {
		t.Errorf("body = %q, want stored + live lines", rec.Body.String())
	}
}

func TestLogsReadBadTryNumber(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/abc", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-integer try_number = %d, want 400", rec.Code)
	}
}
