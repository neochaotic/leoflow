package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func inlineReq(hr *domain.HTTPRequest) Request {
	return Request{Operator: "http_api", TaskID: "t1", HTTPRequest: hr}
}

func TestInlineHTTPRunSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	body, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: http.MethodGet, URL: srv.URL}))
	if err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("Run = %q, err=%v", body, err)
	}
}

func TestInlineHTTPRunNoRequest(t *testing.T) {
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(), inlineReq(nil)); err == nil {
		t.Error("a task with no http_request must error")
	}
}

func TestInlineHTTPRunNon2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: http.MethodGet, URL: srv.URL})); err == nil {
		t.Error("a 500 must fail the task")
	}
}

func TestInlineHTTPRunHonorsCustomSuccessCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418
	}))
	defer srv.Close()
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: http.MethodGet, URL: srv.URL, SuccessStatusCodes: []int{418}})); err != nil {
		t.Errorf("418 should be a success when declared, got %v", err)
	}
}

func TestInlineHTTPRunBuildRequestError(t *testing.T) {
	// An invalid method makes http.NewRequestWithContext fail (a build error).
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: "INVALID METHOD", URL: "http://x"})); err == nil {
		t.Error("an invalid method should produce a build error")
	}
}

func TestInlineHTTPRunMarshalError(t *testing.T) {
	// A body that cannot be JSON-encoded (a channel) fails before sending.
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: http.MethodPost, URL: "http://x", Body: make(chan int)})); err == nil {
		t.Error("an unmarshalable body should error")
	}
}

func TestInlineHTTPRunNetworkError(t *testing.T) {
	if _, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(),
		inlineReq(&domain.HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:1"})); err == nil {
		t.Error("an unreachable host should error")
	}
}

func TestInlineHTTPRunSendsHeadersAndBody(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	_, err := NewInlineHTTPExecutor(nil, 0).Run(context.Background(), inlineReq(&domain.HTTPRequest{
		Method: http.MethodPost, URL: srv.URL,
		Headers: map[string]string{"Authorization": "Bearer xyz"},
		Body:    map[string]any{"k": "v"},
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotAuth != "Bearer xyz" {
		t.Errorf("header not sent, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"k":"v"`) {
		t.Errorf("body not sent as JSON, got %q", gotBody)
	}
}
