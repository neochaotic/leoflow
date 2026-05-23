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
)

type fakeVariableStore struct {
	vars     map[string]domain.Variable
	setCalls []domain.Variable
}

func (f *fakeVariableStore) ListVariables(_ context.Context, _ string, _, _ int) ([]domain.Variable, int, error) {
	out := make([]domain.Variable, 0, len(f.vars))
	for _, v := range f.vars {
		out = append(out, v)
	}
	return out, len(out), nil
}

func (f *fakeVariableStore) GetVariable(_ context.Context, _, key string) (domain.Variable, error) {
	if v, ok := f.vars[key]; ok {
		return v, nil
	}
	return domain.Variable{}, ErrNotFound
}

func (f *fakeVariableStore) SetVariable(_ context.Context, _ string, v domain.Variable) error {
	if f.vars == nil {
		f.vars = map[string]domain.Variable{}
	}
	f.vars[v.Key] = v
	f.setCalls = append(f.setCalls, v)
	return nil
}

func (f *fakeVariableStore) DeleteVariable(_ context.Context, _, key string) error {
	if _, ok := f.vars[key]; !ok {
		return ErrNotFound
	}
	delete(f.vars, key)
	return nil
}

func variablesServer(store VariableStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Variables:     store,
	})
}

func TestVariableCRUD(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{}}
	srv := variablesServer(store)

	// Create.
	rec := authGet(srv, http.MethodPost, "/api/v2/variables", `{"key":"region","value":"us-east","description":"primary"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// List.
	rec = authGet(srv, http.MethodGet, "/api/v2/variables", "")
	var col variableCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || col.Variables[0].Key != "region" || col.Variables[0].Value != "us-east" {
		t.Fatalf("unexpected list: %+v", col)
	}
	if col.Variables[0].IsEncrypted {
		t.Errorf("is_encrypted should be false")
	}
	// Get.
	if r := authGet(srv, http.MethodGet, "/api/v2/variables/region", ""); r.Code != http.StatusOK {
		t.Errorf("get = %d", r.Code)
	}
	// Update.
	rec = authGet(srv, http.MethodPatch, "/api/v2/variables/region", `{"value":"eu-west"}`)
	if rec.Code != http.StatusOK || store.vars["region"].Value != "eu-west" {
		t.Errorf("update = %d, value=%q", rec.Code, store.vars["region"].Value)
	}
	// Delete.
	if r := authGet(srv, http.MethodDelete, "/api/v2/variables/region", ""); r.Code != http.StatusNoContent {
		t.Errorf("delete = %d", r.Code)
	}
	// Missing get/update/delete -> 404.
	if r := authGet(srv, http.MethodGet, "/api/v2/variables/region", ""); r.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", r.Code)
	}
	if r := authGet(srv, http.MethodDelete, "/api/v2/variables/region", ""); r.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", r.Code)
	}
}

func TestVariableSecretMasking(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{
		"db_password": {Key: "db_password", Value: "s3cr3t"},
		"region":      {Key: "region", Value: "us-east"},
	}}
	srv := variablesServer(store)

	// Sensitive key is masked on get.
	rec := authGet(srv, http.MethodGet, "/api/v2/variables/db_password", "")
	var v variableDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Value != "***" {
		t.Errorf("secret value should be masked, got %q", v.Value)
	}
	// Non-sensitive key is shown.
	rec = authGet(srv, http.MethodGet, "/api/v2/variables/region", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Value != "us-east" {
		t.Errorf("plain value should not be masked, got %q", v.Value)
	}
}

func TestVariablesEmptyStubWithoutStore(t *testing.T) {
	rec := authGet(variablesServer(nil), http.MethodGet, "/api/v2/variables", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store = %d", rec.Code)
	}
	var col map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	if col["total_entries"].(float64) != 0 {
		t.Errorf("nil store should yield empty collection, got %v", col)
	}
}
