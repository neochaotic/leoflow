package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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
			if got.Status != tc.ok || got.Message != tc.msg {
				t.Errorf("got %+v, want status=%v msg=%q", got, tc.ok, tc.msg)
			}
			if tester.gotType != "postgres" {
				t.Errorf("tester saw conn_type %q, want postgres", tester.gotType)
			}
		})
	}
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
