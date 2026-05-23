package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeConnStore struct {
	conns    map[string]domain.Connection
	writeErr error
}

func (f *fakeConnStore) ListConnections(_ context.Context, _ string, _, _ int) ([]domain.Connection, int, error) {
	out := make([]domain.Connection, 0, len(f.conns))
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out, len(out), nil
}

func (f *fakeConnStore) GetConnection(_ context.Context, _, id string) (domain.Connection, error) {
	if c, ok := f.conns[id]; ok {
		return c, nil
	}
	return domain.Connection{}, ErrNotFound
}

func (f *fakeConnStore) SetConnection(_ context.Context, _ string, c domain.Connection) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.conns == nil {
		f.conns = map[string]domain.Connection{}
	}
	f.conns[c.ConnID] = c
	return nil
}

func (f *fakeConnStore) DeleteConnection(_ context.Context, _, id string) error {
	if _, ok := f.conns[id]; !ok {
		return ErrNotFound
	}
	delete(f.conns, id)
	return nil
}

func connServer(store ConnectionStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Connections:   store,
	})
}

func TestConnectionCRUDNeverReturnsPassword(t *testing.T) {
	store := &fakeConnStore{conns: map[string]domain.Connection{}}
	srv := connServer(store)

	body := `{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","password":"s3cr3t","schema":"public","port":5432,"extra":"{\"sslmode\":\"require\"}"}`
	rec := authGet(srv, http.MethodPost, "/api/v2/connections", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// The store received the password (to encrypt), but the response must not echo it.
	if store.conns["pg"].Password != "s3cr3t" {
		t.Errorf("store should receive the password to encrypt, got %q", store.conns["pg"].Password)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response leaked the password: %s", rec.Body.String())
	}

	// Get also never returns the password.
	rec = authGet(srv, http.MethodGet, "/api/v2/connections/pg", "")
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Errorf("get leaked the password: %s", rec.Body.String())
	}
	var dto connectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ConnectionID != "pg" || dto.ConnType != "postgres" || dto.Port == nil || *dto.Port != 5432 {
		t.Errorf("unexpected connection dto: %+v", dto)
	}

	// Delete.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/connections/pg", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec := authGet(srv, http.MethodGet, "/api/v2/connections/pg", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", rec.Code)
	}
}

func TestConnectionWriteWithoutKeyReturns503(t *testing.T) {
	// The store reports no encryption key -> the API refuses the write (never
	// stores a credential in plaintext), surfaced as 503.
	store := &fakeConnStore{conns: map[string]domain.Connection{}, writeErr: errors.New("no encryption key configured")}
	rec := authGet(connServer(store), http.MethodPost, "/api/v2/connections",
		`{"connection_id":"pg","conn_type":"postgres","password":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("write without key = %d, want 503", rec.Code)
	}
}

func TestConnectionsEmptyStubWithoutStore(t *testing.T) {
	rec := authGet(connServer(nil), http.MethodGet, "/api/v2/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store = %d", rec.Code)
	}
	var col map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	if col["total_entries"].(float64) != 0 {
		t.Errorf("nil store should yield empty collection, got %v", col)
	}
}
