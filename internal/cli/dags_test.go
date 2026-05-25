package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteDag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deregister  bool
		wantQuery   string
		wantPathEnd string
	}{
		{"clear history", false, "", "/api/v2/dags/etl"},
		{"deregister", true, "deregister=true", "/api/v2/dags/etl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotQuery, gotAuth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			status, _, err := deleteDag(context.Background(), srv.URL, "tok", "etl", tc.deregister)
			if err != nil {
				t.Fatalf("deleteDag: %v", err)
			}
			if status != http.StatusNoContent {
				t.Errorf("status = %d", status)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", gotMethod)
			}
			if gotPath != tc.wantPathEnd {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPathEnd)
			}
			if gotQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tc.wantQuery)
			}
			if gotAuth != "Bearer tok" {
				t.Errorf("auth = %q, want Bearer tok", gotAuth)
			}
		})
	}
}
