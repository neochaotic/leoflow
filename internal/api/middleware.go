package api

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

const (
	contextKeyUser      = "leoflow.user"
	contextKeyRequestID = "leoflow.request_id"
	headerRequestID     = "X-Request-Id"
)

// RequestID assigns a request id (honoring an inbound X-Request-Id) and echoes it.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = newRequestID()
		}
		c.Set(contextKeyRequestID, id)
		c.Header(headerRequestID, id)
		c.Next()
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// StructuredLogger logs one structured line per request (ADR 0010).
func StructuredLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http request",
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString(contextKeyRequestID),
		)
	}
}

// CORS allows the configured origins (use "*" to allow any).
func CORS(allowed []string) gin.HandlerFunc {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (set["*"] || set[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

var publicPrefixes = []string{"/auth/", "/healthz", "/readyz", "/metrics", "/docs", "/openapi", "/ui/config"}

func isPublic(path string) bool {
	for _, p := range publicPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// JWTAuth validates the bearer token on protected routes and stores the user.
func JWTAuth(authn auth.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isPublic(c.Request.URL.Path) {
			c.Next()
			return
		}
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		user, err := authn.Authenticate(c.Request.Context(), token)
		if err != nil {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		c.Set(contextKeyUser, user)
		c.Next()
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimPrefix(header, prefix)
	}
	return ""
}

// UserFromContext returns the authenticated user stored by JWTAuth.
func UserFromContext(c *gin.Context) (*auth.User, bool) {
	v, ok := c.Get(contextKeyUser)
	if !ok {
		return nil, false
	}
	u, ok := v.(*auth.User)
	return u, ok
}

// RequirePermission enforces an RBAC permission on a route.
func RequirePermission(action, resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		if !user.HasPermission(action, resource) {
			AbortProblem(c, http.StatusForbidden, "forbidden", "missing permission "+action+":"+resource)
			return
		}
		c.Next()
	}
}
