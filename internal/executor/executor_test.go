package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeExecutor struct{ name string }

func (f *fakeExecutor) Execute(context.Context, Request) error { return nil }

func TestRouterRoutesHTTPInline(t *testing.T) {
	std := &fakeExecutor{name: "standard"}
	inline := &fakeExecutor{name: "inline"}
	r := NewRouter(std, inline)

	if got := r.ExecutorFor("http_api"); got != inline {
		t.Error("http_api should route to the inline executor")
	}
	if got := r.ExecutorFor("python"); got != std {
		t.Error("python should route to the standard executor")
	}
	if got := r.ExecutorFor("bash"); got != std {
		t.Error("bash should route to the standard executor")
	}
}

func TestRouterFallsBackWhenNoInline(t *testing.T) {
	std := &fakeExecutor{name: "standard"}
	r := NewRouter(std, nil)
	if got := r.ExecutorFor("http_api"); got != std {
		t.Error("with no inline executor, http_api falls back to standard")
	}
}

func httpReq(url, method string, codes []int) Request {
	return Request{
		Operator:    "http_api",
		HTTPRequest: &domain.HTTPRequest{Method: method, URL: url, SuccessStatusCodes: codes, TimeoutSeconds: 5},
	}
}

func TestInlineHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := NewInlineHTTPExecutor(nil, 2).Execute(context.Background(), httpReq(srv.URL, http.MethodGet, nil)); err != nil {
		t.Errorf("2xx should succeed: %v", err)
	}
}

func TestInlineHTTPRetriesThenFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := NewInlineHTTPExecutor(nil, 2).Execute(context.Background(), httpReq(srv.URL, http.MethodPost, nil))
	if err == nil {
		t.Error("persistent 500 should fail")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", got)
	}
}

func TestInlineHTTPCustomSuccessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	if err := NewInlineHTTPExecutor(nil, 0).Execute(context.Background(), httpReq(srv.URL, http.MethodGet, []int{http.StatusTeapot})); err != nil {
		t.Errorf("418 should succeed when listed as a success code: %v", err)
	}
}

func TestInlineHTTPMissingRequest(t *testing.T) {
	if err := NewInlineHTTPExecutor(nil, 0).Execute(context.Background(), Request{Operator: "http_api"}); err == nil {
		t.Error("missing http_request should error")
	}
}
