package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestObservabilityHandler pins the surface the metrics listener serves in every
// role (ADR 0049): /metrics plus the same /healthz and /readyz the full API
// exposes. The scheduler role serves no API, so this is the only liveness/
// readiness surface its pod's kubelet probes can reach — it must behave exactly
// like the API's copy (trivial liveness; readiness pings every dependency).
func TestObservabilityHandler(t *testing.T) {
	reg := prometheus.NewRegistry()

	do := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))
		return rec
	}

	t.Run("healthz is trivial 200", func(t *testing.T) {
		h := ObservabilityHandler(reg, map[string]HealthChecker{"postgres": &fakeHealthCheck{err: errors.New("down")}})
		// Liveness must NOT reach dependencies: a down postgres still returns 200
		// so kubelet does not restart a healthy scheduler over a flaky datastore.
		if rec := do(h, "/healthz"); rec.Code != http.StatusOK {
			t.Fatalf("/healthz = %d, want 200", rec.Code)
		}
	})

	t.Run("readyz reflects dependency health", func(t *testing.T) {
		ok := ObservabilityHandler(reg, map[string]HealthChecker{"postgres": &fakeHealthCheck{}})
		if rec := do(ok, "/readyz"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ready") {
			t.Fatalf("/readyz healthy = %d %q, want 200 ready", rec.Code, rec.Body.String())
		}
		bad := ObservabilityHandler(reg, map[string]HealthChecker{"postgres": &fakeHealthCheck{err: errors.New("connection refused")}})
		rec := do(bad, "/readyz")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz degraded = %d, want 503", rec.Code)
		}
		// Names the failed dep, but must not leak the raw error (audit H2).
		if !strings.Contains(rec.Body.String(), "postgres") {
			t.Errorf("/readyz body should name the failed dep, got %q", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "connection refused") {
			t.Errorf("/readyz leaked the raw dependency error, got %q", rec.Body.String())
		}
	})

	t.Run("metrics is served", func(t *testing.T) {
		h := ObservabilityHandler(reg, nil)
		if rec := do(h, "/metrics"); rec.Code != http.StatusOK {
			t.Fatalf("/metrics = %d, want 200", rec.Code)
		}
	})

	t.Run("nil registry omits metrics but keeps health", func(t *testing.T) {
		h := ObservabilityHandler(nil, nil)
		if rec := do(h, "/healthz"); rec.Code != http.StatusOK {
			t.Errorf("/healthz with nil registry = %d, want 200", rec.Code)
		}
	})
}
