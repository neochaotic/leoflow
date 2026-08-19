package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

// breakGlass gates the credential path when the provider is OIDC (D8): only the
// designated local admin email(s) may log in with a password; every other
// password login is rejected, and each attempt is audited (H5). It is nil in
// JWT mode, where the credential path is ungated and unchanged.
type breakGlass struct {
	emails map[string]bool
	audit  AuthAuditWriter
}

// newBreakGlass builds the gate from the allowlist. It returns nil for an empty
// allowlist so the caller keeps the ungated behavior (JWT mode, or an OIDC
// deployment that configured no break-glass account).
func newBreakGlass(emails []string, audit AuthAuditWriter) *breakGlass {
	if len(emails) == 0 {
		return nil
	}
	set := make(map[string]bool, len(emails))
	for _, e := range emails {
		set[strings.ToLower(strings.TrimSpace(e))] = true
	}
	return &breakGlass{emails: set, audit: audit}
}

// allows reports whether the email may use the credential path.
func (b *breakGlass) allows(email string) bool {
	return b.emails[strings.ToLower(strings.TrimSpace(email))]
}

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
// When bg is non-nil (OIDC mode, D8) only the break-glass allowlist may use the
// credential path; every other password login is rejected and audited, so
// enabling SSO does not silently leave a full password bypass open.
func authTokenHandler(authn auth.Authenticator, limiter *auth.RateLimiter, ttlSeconds int, bg *breakGlass) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Peek the limiter up front but DON'T count this attempt yet: only failed
		// logins consume the budget (recorded below). This is what keeps a user who
		// mistypes a couple times — or whose SPA re-fetches a token — from getting
		// locked out the instant they finally send the right password.
		if limiter.Blocked(c.ClientIP()) {
			AbortProblem(c, http.StatusTooManyRequests, "rate limited",
				"too many failed login attempts; wait about a minute and try again")
			return
		}
		var req tokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		tenant := strings.TrimSpace(req.Tenant)
		if tenant == "" {
			tenant = "default"
		}
		username := strings.TrimSpace(req.Username)
		// Break-glass gate (D8): under OIDC, reject any password login that is not
		// on the designated local-admin allowlist, before touching the credential
		// store. Audited either way.
		if bg != nil && !bg.allows(username) {
			recordBreakGlass(c, bg, tenant, username, "denied")
			limiter.Allow(c.ClientIP())
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}
		// Trim the username/tenant (emails carry no meaningful surrounding space and
		// autofill/paste often add one), but NEVER the password — trailing/leading
		// spaces are valid password characters, and silently trimming them is unsafe.
		token, err := authn.IssueToken(c.Request.Context(), auth.Credentials{
			Tenant:   tenant,
			Username: username,
			Password: req.Password,
		})
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				limiter.Allow(c.ClientIP()) // count the failure toward the lockout budget
				if bg != nil {
					recordBreakGlass(c, bg, tenant, username, "bad_credentials")
				}
				AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
				return
			}
			AbortProblem(c, http.StatusInternalServerError, "internal error", "could not issue token")
			return
		}
		if bg != nil {
			recordBreakGlass(c, bg, tenant, username, "success")
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: ttlSeconds})
	}
}

// recordBreakGlass audits a break-glass credential-path attempt (H5),
// best-effort so a flaky audit sink never changes the login outcome.
func recordBreakGlass(c *gin.Context, bg *breakGlass, tenant, email, outcome string) {
	if bg == nil || bg.audit == nil {
		return
	}
	//nolint:errcheck // best-effort audit; a sink error must never fail the login
	_ = bg.audit.RecordAuthEvent(c.Request.Context(), tenant, "", auditBreakGlass, email, outcome, nil)
}
