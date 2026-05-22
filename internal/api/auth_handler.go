package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

type tokenRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Tenant   string `json:"tenant"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// authTokenHandler issues a JWT for valid credentials, rate-limited per client IP.
func authTokenHandler(authn auth.Authenticator, limiter *auth.RateLimiter, ttlSeconds int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Allow(c.ClientIP()) {
			AbortProblem(c, http.StatusTooManyRequests, "rate limited", "too many login attempts")
			return
		}
		var req tokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		tenant := req.Tenant
		if tenant == "" {
			tenant = "default"
		}
		token, err := authn.IssueToken(c.Request.Context(), auth.Credentials{
			Tenant:   tenant,
			Username: req.Username,
			Password: req.Password,
		})
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
				return
			}
			AbortProblem(c, http.StatusInternalServerError, "internal error", "could not issue token")
			return
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: ttlSeconds})
	}
}
