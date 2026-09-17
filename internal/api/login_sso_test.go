package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// loginPage renders the login page the way the router does, for a server with or
// without an OIDC flow discovered.
func loginPage(t *testing.T, sso bool) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/login", loginPageHandler(sso))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/login?next=/dags", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("login page = %d", rec.Code)
	}
	return rec.Body.String()
}

// TestLoginPageOffersTheSSOFlowWhenItExists is #1160.
//
// The server has had complete OIDC since ADR 0057 and the chart can now configure
// it, but the login page was a fixed username and password form with no branch
// anywhere: nothing read the configured provider, and nothing linked to
// /api/v2/auth/oidc/login, which is the route that starts the flow.
//
// So a correctly configured SSO deployment showed a password form. Users typed
// their IdP credentials into it and got "Invalid credentials", which is
// indistinguishable from a misconfiguration and sends every diagnostic instinct
// at the IdP. It also produced no audit record at all, because no OIDC request
// was ever made.
func TestLoginPageOffersTheSSOFlowWhenItExists(t *testing.T) {
	body := loginPage(t, true)

	if !strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Fatal("the login page offers no way to start the OIDC flow; a user configured for SSO has no route into it but the address bar")
	}
	// The post-login destination has to survive the detour through the IdP, or SSO
	// users always land on the root while password users keep their deep link.
	// Assert the VALUE, not just the parameter's presence. An earlier version
	// escaped next here and html/template escaped it again in the href context,
	// producing next=%252Fdags; the OIDC handler then read a literal "%2Fdags",
	// sanitizeNext refused it as not absolute, and every SSO login landed on the
	// root. "?next=" alone was true throughout that bug.
	if !strings.Contains(body, "/api/v2/auth/oidc/login?next=%2fdags") &&
		!strings.Contains(body, "/api/v2/auth/oidc/login?next=%2Fdags") {
		t.Errorf("the SSO link does not carry a usable next value; an SSO login cannot return the user to the page they asked for:\n%s", body)
	}
	if strings.Contains(body, "%252F") || strings.Contains(body, "%252f") {
		t.Error("next is double-escaped, so sanitizeNext will refuse it and the user lands on the root")
	}
	// break_glass_emails exists precisely so named local logins still work when the
	// IdP is down or the tenant pin is wrong. Hiding the form hides the escape
	// hatch at the moment it is needed.
	if !strings.Contains(body, `name="password"`) {
		t.Error("the password form is gone; break-glass accounts have no way in when the IdP is unreachable")
	}
}

// TestLoginPageHidesSSOWhenThereIsNone guards the other direction: a JWT-only
// deployment must not advertise a route its router never registered, which would
// 404 the user.
func TestLoginPageHidesSSOWhenThereIsNone(t *testing.T) {
	body := loginPage(t, false)

	if strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Error("a JWT-only deployment advertises an OIDC route that is not registered; following it 404s")
	}
	if !strings.Contains(body, `name="password"`) {
		t.Error("the password form must be there when it is the only way in")
	}
}
