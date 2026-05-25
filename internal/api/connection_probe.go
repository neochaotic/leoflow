package api

import (
	"context"
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
