package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// ConnectionTester checks whether a connection's endpoint is usable. The default
// implementation tests reachability (TCP dial / HTTP) from the control plane;
// full credential/provider validation would need the provider hooks (Python),
// which the Go control plane does not run.
type ConnectionTester interface {
	Test(ctx context.Context, c domain.Connection) (ok bool, message string)
}

// connectionTestResultDTO is the Airflow ConnectionTestResponse shape.
type connectionTestResultDTO struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
}

// testConnectionHandler powers POST /api/v2/connections/test (the panel's "Test"
// button). It tests the posted connection body without persisting anything.
func testConnectionHandler(tester ConnectionTester) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid connection body"})
			return
		}
		ok, msg := tester.Test(c.Request.Context(), body.toDomain(body.ConnectionID))
		c.JSON(http.StatusOK, connectionTestResultDTO{Status: ok, Message: msg})
	}
}

// defaultConnPorts maps connection types to their well-known port, used when the
// connection does not pin one.
var defaultConnPorts = map[string]int{
	"postgres": 5432, "redshift": 5439, "mysql": 3306, "mariadb": 3306,
	"redis": 6379, "mongo": 27017, "mssql": 1433, "oracle": 1521,
	"ftp": 21, "sftp": 22, "ssh": 22, "kafka": 9092,
}

// defaultConnectionTester tests endpoint reachability using only the stdlib.
type defaultConnectionTester struct{}

// Test reports whether the connection's endpoint is reachable from the control
// plane. HTTP(S) connections get a GET; everything else gets a TCP dial to
// host:port (the connection's port, else the type's default).
func (defaultConnectionTester) Test(ctx context.Context, conn domain.Connection) (ok bool, message string) {
	t := strings.ToLower(conn.ConnType)
	if t == "http" || t == "https" {
		return testHTTPReachable(ctx, conn)
	}
	if t == "google_cloud_platform" {
		return testGCPConnection(conn)
	}
	if conn.Host == "" {
		return false, "no host configured to test"
	}
	port := 0
	if conn.Port != nil {
		port = *conn.Port
	}
	if port == 0 {
		port = defaultConnPorts[t]
	}
	if port == 0 {
		return false, "set a port to test reachability for conn_type " + conn.ConnType
	}
	addr := net.JoinHostPort(conn.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 5 * time.Second}
	cn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, "cannot reach " + addr + ": " + err.Error()
	}
	_ = cn.Close() //nolint:errcheck // reachability probe; close result is irrelevant
	return true, "reachable: " + addr
}

// testGCPConnection structurally validates a google_cloud_platform connection
// from the control plane. It does NOT call Google: the control plane runs no
// provider hooks (Python), and reachability is meaningless for GCP (no host).
// It validates the service-account key shape when a key is present, or reports
// keyless (ADC / Workload Identity), which is resolved at task runtime. Short
// field names are preferred, with Airflow's legacy extra__google_cloud_platform__
// names accepted as a fallback.
func testGCPConnection(conn domain.Connection) (ok bool, message string) {
	extra := map[string]any{}
	if conn.Extra != "" {
		if err := json.Unmarshal([]byte(conn.Extra), &extra); err != nil {
			return false, "invalid Extra JSON: " + err.Error()
		}
	}
	field := func(name string) any {
		if v, present := extra[name]; present && v != nil && v != "" {
			return v
		}
		return extra["extra__google_cloud_platform__"+name]
	}

	if kd := field("keyfile_dict"); kd != nil {
		return validateGCPKeyfileDict(kd)
	}
	if kp, isStr := field("key_path").(string); isStr && kp != "" {
		return true, "key_path set — validated at task runtime: " + kp
	}
	return true, "keyless (ADC / Workload Identity) — resolved at task runtime, not from the control plane"
}

// validateGCPKeyfileDict structurally validates an inline service-account key
// (the keyfile_dict field). It accepts the key either as a nested JSON object or
// as a stringified JSON blob (Airflow emits both forms) and checks the fields
// every service-account key carries. It performs no network call — the actual
// token exchange happens in the task at runtime.
func validateGCPKeyfileDict(kd any) (ok bool, message string) {
	var key map[string]any
	switch v := kd.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &key); err != nil {
			return false, "keyfile_dict is not valid JSON: " + err.Error()
		}
	case map[string]any:
		key = v
	default:
		return false, "keyfile_dict must be a JSON object or a JSON string"
	}
	// Capture each required field via an ok-checked type assertion (the A+ linter
	// forbids discarding the assertion's ok value).
	got := make(map[string]string, 4)
	for _, name := range []string{"type", "client_email", "private_key", "project_id"} {
		s, isStr := key[name].(string)
		if !isStr || s == "" {
			return false, "keyfile_dict missing required field: " + name
		}
		got[name] = s
	}
	if got["type"] != "service_account" {
		return false, `keyfile_dict "type" must be "service_account"`
	}
	return true, "service-account key looks valid (" + got["client_email"] + ")"
}

func testHTTPReachable(ctx context.Context, conn domain.Connection) (ok bool, message string) {
	target := conn.Host
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		scheme := "http"
		if strings.EqualFold(conn.ConnType, "https") {
			scheme = "https"
		}
		target = scheme + "://" + conn.Host
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return false, "invalid host: " + err.Error()
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false, "request failed: " + err.Error()
	}
	_ = resp.Body.Close() //nolint:errcheck // reachability probe
	return resp.StatusCode < http.StatusInternalServerError, "HTTP " + strconv.Itoa(resp.StatusCode)
}
