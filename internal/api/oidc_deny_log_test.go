package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/neochaotic/leoflow/internal/oidc"
)

func denyLog(t *testing.T, run func(d oidcDeps, c *gin.Context)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	d := oidcDeps{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v2/auth/oidc/callback", nil)
	run(d, c)
	return buf.String()
}

// TestEveryDenyReachesTheLog is the diagnosability half of the SSO audit.
//
// Every fail-closed path funnels through deny, which recorded an audit row and
// wrote a 403 and logged nothing. The audit row is the record of truth, but it
// lives in a Postgres table reachable only through an API that needs a working
// session, and with provider: oidc and an empty break-glass list there is no
// session to be had. So the one channel an operator can actually read carried
// nothing at all: a deployment rejecting 100% of logins looked, in the logs,
// exactly like a deployment nobody was trying to use.
func TestEveryDenyReachesTheLog(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.deny(c, auditOIDCLoginFailure, "default", "", "someone@example.com", "no_user_jit_off")
	})

	if !strings.Contains(out, "no_user_jit_off") {
		t.Errorf("the deny reason never reaches the log, so the only copy is in a table an SSO-locked-out operator cannot query:\n%s", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Errorf("the deny is not logged at WARN; a rejected login is not a debug detail:\n%s", out)
	}
}

// TestTokenInvalidCarriesTheUnderlyingError is the worst of the diagnosability
// holes. verifyReason collapses everything it does not recognize into
// token_invalid: a JWKS fetch failure, a network timeout to the IdP, an audience
// mismatch, a signature surprise. The reason is stable and non-secret, which is
// right for an audit row, but the real error was discarded entirely, so a
// transient IdP outage and a wrong client_id were indistinguishable.
func TestTokenInvalidCarriesTheUnderlyingError(t *testing.T) {
	underlying := errors.New("oidc: expected audience \"right-client\" got [\"wrong-client\"]")

	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.rejectVerify(c, underlying)
	})

	if !strings.Contains(out, "token_invalid") {
		t.Fatalf("the audit reason is missing from the log:\n%s", out)
	}
	if !strings.Contains(out, "expected audience") {
		t.Errorf("token_invalid is logged without the error that produced it, so every unrecognized failure is indistinguishable:\n%s", out)
	}
}

// TestRecognizedReasonsDoNotLeakTokenText guards the other direction. A reason
// verifyReason recognizes is already self-describing, and the underlying error
// can carry claim values, so it must not be appended there.
func TestRecognizedReasonsDoNotLeakTokenText(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.rejectVerify(c, oidc.ErrTenantNotAllowed)
	})

	if !strings.Contains(out, "tenant_not_allowed") {
		t.Fatalf("the reason is missing:\n%s", out)
	}
	if strings.Contains(out, "error=") {
		t.Errorf("a recognized reason attached the raw error, which can carry claim values:\n%s", out)
	}
}
