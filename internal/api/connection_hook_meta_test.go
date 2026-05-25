package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
)

// The connection-type catalog must render so the Add/Edit form is not empty: it
// drives every standard field. Regression for the "edit shows empty" bug.
func TestConnectionHookMeta(t *testing.T) {
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
	rec := authGet(srv, http.MethodGet, "/ui/connections/hook_meta", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("hook_meta = %d", rec.Code)
	}
	var meta []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	var postgres map[string]any
	for _, m := range meta {
		if m["connection_type"] == "postgres" {
			postgres = m
		}
	}
	if postgres == nil {
		t.Fatalf("catalog missing postgres; got %d types", len(meta))
	}
	if postgres["hook_name"] != "Postgres" {
		t.Errorf("postgres hook_name = %v", postgres["hook_name"])
	}
	// standard_fields must carry the fields the form renders, incl. url_schema.
	sf, _ := postgres["standard_fields"].(map[string]any)
	for _, f := range []string{"host", "login", "password", "port", "url_schema", "description"} {
		if _, ok := sf[f]; !ok {
			t.Errorf("postgres standard_fields missing %q", f)
		}
	}
}
