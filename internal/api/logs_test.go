package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeLogReader struct {
	body string
	err  error
}

func (f *fakeLogReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
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

func TestLogsReadNotFound(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{err: domain.ErrNotFound}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing logs = %d, want 404", rec.Code)
	}
}

func TestLogsReadBadTryNumber(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/abc", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-integer try_number = %d, want 400", rec.Code)
	}
}
