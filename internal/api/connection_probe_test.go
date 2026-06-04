package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeConnTester struct {
	ok      bool
	message string
	gotType string
}

func (f *fakeConnTester) Test(_ context.Context, c domain.Connection) (ok bool, message string) {
	f.gotType = c.ConnType
	return f.ok, f.message
}

func probeServer(tester ConnectionTester) *gin.Engine {
	return NewServer(Dependencies{
		Logger:         discardLogger(),
		Authenticator:  &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:    auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:    []string{"*"},
		ConnectionTest: tester,
	})
}

func TestConnectionTestEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		msg  string
	}{
		{"reachable", true, "reachable: db:5432"},
		{"unreachable", false, "cannot reach db:5432: timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := &fakeConnTester{ok: tc.ok, message: tc.msg}
			rec := authGet(probeServer(tester), http.MethodPost, "/api/v2/connections/test",
				`{"connection_id":"c","conn_type":"postgres","host":"db"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
			}
			var got connectionTestResultDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			// The reachability result is preserved verbatim …
			if got.Status != tc.ok || !strings.Contains(got.Message, tc.msg) {
				t.Errorf("got %+v, want status=%v msg containing %q", got, tc.ok, tc.msg)
			}
			// … and a connector nudge is appended for a known conn_type, since the
			// probe response is our surface (the form itself is Airflow's SPA). This
			// is the setup-time reminder that the Connection alone is not enough —
			// the DAG must declare the provider. ADR 0038 #1.
			if !strings.Contains(got.Message, "connectors: [postgres]") {
				t.Errorf("message %q missing the connectors: nudge", got.Message)
			}
			if tester.gotType != "postgres" {
				t.Errorf("tester saw conn_type %q, want postgres", tester.gotType)
			}
		})
	}
}

// TestConnectionTestNudgeOnlyForKnownConnectors guards that the nudge is keyed on
// the catalog: a curated conn_type gets the connectors: hint; an unknown one does
// not (no misleading suggestion for a type the sugar cannot expand).
func TestConnectionTestNudgeOnlyForKnownConnectors(t *testing.T) {
	tester := &fakeConnTester{ok: true, message: "reachable: x:1"}
	rec := authGet(probeServer(tester), http.MethodPost, "/api/v2/connections/test",
		`{"connection_id":"c","conn_type":"totally_made_up","host":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	var got connectionTestResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Message, "connectors:") {
		t.Errorf("unknown conn_type should get no nudge; message = %q", got.Message)
	}
}

// The GCP probe is structural (no cloud call): a well-formed service-account key
// validates, a malformed/incomplete one fails with a clear message, and an empty
// (keyless) connection reports ADC/Workload Identity as ok.
func TestGCPConnectionProbe(t *testing.T) {
	validKey := `{"type":"service_account","client_email":"x@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nABC\n-----END PRIVATE KEY-----\n","project_id":"p"}`
	for _, tc := range []struct {
		name    string
		extra   string
		wantOK  bool
		wantSub string // substring expected in the message
	}{
		{"keyfile_dict object", `{"keyfile_dict":` + validKey + `}`, true, "service-account key looks valid"},
		{"keyfile_dict stringified", `{"keyfile_dict":` + jsonString(validKey) + `}`, true, "service-account key looks valid"},
		{"legacy extra name", `{"extra__google_cloud_platform__keyfile_dict":` + validKey + `}`, true, "looks valid"},
		{"invalid extra json", `{not json`, false, "invalid Extra JSON"},
		{"missing field", `{"keyfile_dict":{"type":"service_account","client_email":"x","project_id":"p"}}`, false, "missing required field: private_key"},
		{"wrong type", `{"keyfile_dict":{"type":"user","client_email":"x","private_key":"k","project_id":"p"}}`, false, `must be "service_account"`},
		{"key_path", `{"key_path":"/etc/gcp/key.json"}`, true, "key_path set"},
		{"keyless empty", ``, true, "keyless"},
		{"keyless explicit", `{"project":"p","scopes":["https://www.googleapis.com/auth/cloud-platform"]}`, true, "keyless"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := testGCPConnection(domain.Connection{ConnType: "google_cloud_platform", Extra: tc.extra})
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Errorf("msg = %q, want substring %q", msg, tc.wantSub)
			}
		})
	}
}

// jsonString returns s as a JSON string literal (quoted + escaped).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The built-in tester reports reachable for a listening port and unreachable for
// a closed one (TCP-dial reachability).
func TestDefaultConnectionTesterReachability(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot open a local listener:", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ok, msg := defaultConnectionTester{}.Test(context.Background(),
		domain.Connection{ConnType: "postgres", Host: "127.0.0.1", Port: &port})
	if !ok {
		t.Errorf("listening port should be reachable, got %q", msg)
	}

	_ = ln.Close() // closing the listener kills the port
	ok2, _ := defaultConnectionTester{}.Test(context.Background(),
		domain.Connection{ConnType: "postgres", Host: "127.0.0.1", Port: &port})
	if ok2 {
		t.Errorf("closed port should be unreachable")
	}
}

func TestHTTPReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// The httptest URL already carries the http:// scheme.
	if ok, msg := testHTTPReachable(context.Background(), domain.Connection{ConnType: "http", Host: srv.URL}); !ok || msg != "HTTP 200" {
		t.Errorf("200 should be reachable, got ok=%v msg=%q", ok, msg)
	}

	// A 5xx is treated as unreachable (the endpoint is up but unhealthy).
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	if ok, _ := testHTTPReachable(context.Background(), domain.Connection{ConnType: "http", Host: bad.URL}); ok {
		t.Error("a 502 should be reported unreachable")
	}

	// A bare host (no scheme) gets http:// prepended and still resolves.
	host := strings.TrimPrefix(srv.URL, "http://")
	if ok, msg := testHTTPReachable(context.Background(), domain.Connection{ConnType: "http", Host: host}); !ok {
		t.Errorf("scheme should be prepended for a bare host, got ok=%v msg=%q", ok, msg)
	}

	// A host that cannot connect fails cleanly (no panic), reporting the failure.
	if ok, msg := testHTTPReachable(context.Background(), domain.Connection{ConnType: "http", Host: "127.0.0.1:1"}); ok || msg == "" {
		t.Errorf("an unreachable host should fail with a message, got ok=%v msg=%q", ok, msg)
	}
}
