package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/xcom"
)

type fakeXComReader struct {
	entry xcom.Entry
	err   error
}

func (f *fakeXComReader) GetXCom(context.Context, string, string, string, string, string) (xcom.Entry, error) {
	return f.entry, f.err
}

func xcomServer(reader XComReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Xcoms:         reader,
	})
}

func TestXComReadReturnsEntry(t *testing.T) {
	reader := &fakeXComReader{entry: xcom.Entry{Value: []byte(`{"rows":100}`), ContentType: "application/json", CreatedAt: time.Now()}}
	rec := authGet(xcomServer(reader), http.MethodGet, "/api/v2/xcoms/etl/run-1/extract/return_value", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("xcom read = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var dto xComEntryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if string(dto.Value) != `{"rows":100}` || dto.Key != "return_value" || dto.TaskID != "extract" || dto.MapIndex != -1 {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

func TestXComReadNotFound(t *testing.T) {
	reader := &fakeXComReader{err: domain.ErrNotFound}
	rec := authGet(xcomServer(reader), http.MethodGet, "/api/v2/xcoms/etl/run-1/extract/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing xcom = %d, want 404", rec.Code)
	}
}
