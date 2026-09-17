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
	// Default to a deployment WITH break-glass accounts: that is the posture the
	// chart documents, and the one the existing assertions are about.
	return loginPageWith(t, sso, sso)
}

func loginPageWith(t *testing.T, sso, breakGlass bool) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/login", loginPageHandler(sso, breakGlass))
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

// TestLoginPageDoesNotPresentAFormNobodyCanUse covers the trap in the default SSO
// configuration.
//
// Under provider: oidc with break_glass_emails empty, newBreakGlass returns a
// gate that admits NOBODY: every password login is rejected, by design, because
// that is what SSO-only means. The page nonetheless rendered a full username and
// password form as its primary control, with the focus in it. A user who types
// there gets "Invalid credentials", which is the same answer a wrong password
// gets, so the page actively teaches the wrong diagnosis.
//
// The form stays reachable, because an operator adding a break-glass account
// wants it, but it is no longer presented as a way in, and it says why.
func TestLoginPageDoesNotPresentAFormNobodyCanUse(t *testing.T) {
	body := loginPageWith(t, true, false)

	if !strings.Contains(body, `name="password"`) {
		t.Fatal("the form is gone entirely; adding a break-glass account must not require a new release")
	}
	if !strings.Contains(body, "<details") {
		t.Error("the form nobody can use is still the page's primary control")
	}
	if strings.Contains(body, "autofocus") {
		t.Error("focus lands in a field where no value can succeed, and a password manager will autofill it")
	}
	if !strings.Contains(body, "break_glass_emails") {
		t.Error("the page does not name the setting that makes this form work, which is the one thing the operator reading it needs")
	}
}

// TestLoginPageKeepsTheFormWhenBreakGlassAccountsExist is the other direction:
// with an allowlist configured the form is a working escape hatch for exactly
// the moment SSO is broken, so it must not be buried.
func TestLoginPageKeepsTheFormWhenBreakGlassAccountsExist(t *testing.T) {
	body := loginPageWith(t, true, true)

	if strings.Contains(body, "<details") {
		t.Error("the break-glass form is collapsed on a deployment where it works, at the moment the IdP is down and it is the only way in")
	}
	if !strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Error("the SSO button is gone")
	}
}

// TestLoginPageFocusesTheFormWhenItIsTheOnlyWayIn keeps the JWT deployment's
// behavior: there the form is the login, so the focus belongs in it.
func TestLoginPageFocusesTheFormWhenItIsTheOnlyWayIn(t *testing.T) {
	if !strings.Contains(loginPageWith(t, false, false), "autofocus") {
		t.Error("a JWT-only login page no longer focuses its username field")
	}
}
