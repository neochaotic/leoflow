package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// One place decides the session cookie's attributes, because two places decided
// them differently and nobody noticed for a release.
//
// The OIDC callback set _token server-side, HttpOnly. The password login page
// set the same cookie from a script (document.cookie), which has two
// consequences a reader of either path alone would never see.
//
// A script cannot overwrite an HttpOnly cookie. So a password login on top of a
// live SSO session was dropped by the browser: the server minted a token and
// answered 200, the page navigated away, and the browser kept sending the old
// SSO _token. The UI went on showing the previous identity while every log said
// the login succeeded. That is the worst possible moment for it, because the
// password path under SSO is break-glass: it is used when SSO is already broken
// and the operator is already unsure what works.
//
// And a cookie a script can write is a cookie a script can read. A deployment
// that never turned SSO on never got the HttpOnly one at all, so its session
// token was readable by anything running on the page. The two paths mint the
// same session; they now protect it identically.

// sessionCookiePath scopes the session cookie to the whole origin. The UI, the
// API and the IDE all read it, so a narrower path would authenticate some of
// them and not others. It is equally the path logout must clear on: a browser
// matches a deletion by name, domain and path, so a mismatch there leaves the
// session alive behind a redirect that claims it ended.
const sessionCookiePath = "/"

// setSessionCookie writes the _token session cookie server-side. Both login
// paths call it, so both get HttpOnly, SameSite=Lax and the same path and
// lifetime, and clearSessionCookie deletes exactly what it wrote.
//
// insecure drops the Secure attribute; see cookieSecure.
func setSessionCookie(c *gin.Context, token string, ttl time.Duration, insecure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authTokenCookie, token, int(ttl.Seconds()), sessionCookiePath, "", cookieSecure(insecure), true)
}

// clearSessionCookie expires the session cookie. Every attribute a browser
// matches on is setSessionCookie's, including Secure: a Set-Cookie carrying
// Secure is itself refused over plain http, so a deletion that disagreed with
// the cookie it is deleting would be silently dropped on exactly the deployment
// the disagreement came from.
func clearSessionCookie(c *gin.Context, insecure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authTokenCookie, "", -1, sessionCookiePath, "", cookieSecure(insecure), true)
}

// cookieSecure reports whether the auth cookies carry Secure. It is true unless
// the operator set auth.session_cookie_insecure.
//
// The server cannot derive this from the request. Behind a TLS-terminating
// ingress it sees plain http while the browser sees https, so deriving Secure
// from the request scheme would strip it from the majority deployment, which is
// the one that most needs it. Deriving it from X-Forwarded-Proto moves the
// decision to a header that is only trustworthy when trusted_proxies is
// configured, and the default is to trust none. So it is a setting, defaulting
// to the safe value, with one documented reason to change it: a deployment
// served over plain http to something that is not a loopback address, where a
// browser refuses a Secure cookie outright and login would otherwise fail with
// no visible error.
func cookieSecure(insecure bool) bool { return !insecure }
