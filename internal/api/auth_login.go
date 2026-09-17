package api

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The Airflow 3.2.1 UI sends an unauthenticated user to GET /api/v2/auth/login.
// Upstream this redirects into the simple-auth-manager login SPA, which POSTs
// credentials to /auth/token and stores the returned JWT in the "_token" cookie
// (path /) that the rest of the UI reads. Rather than embed that second SPA,
// Leoflow serves a minimal login page honoring the same contract: it POSTs
// /auth/token and sets the _token cookie, then returns to `next`. See
// docs/ui-compatibility.md and ADR 0018.

// loginPageTemplate is a self-contained login form. Its script posts to
// /auth/token, stores the JWT in the _token cookie, and navigates to next.
var loginPageTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Leoflow — Sign in</title>
<style>
 body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;
   min-height:100vh;align-items:center;justify-content:center;margin:0}
 form{background:#1e293b;padding:2rem;border-radius:12px;width:320px;box-shadow:0 10px 30px rgba(0,0,0,.4)}
 h1{font-size:1.25rem;margin:0 0 1rem}
 label{display:block;font-size:.8rem;margin:.75rem 0 .25rem;color:#94a3b8}
 input{width:100%;padding:.6rem;border-radius:6px;border:1px solid #334155;background:#0f172a;color:#e2e8f0;box-sizing:border-box}
 button{margin-top:1.25rem;width:100%;padding:.65rem;border:0;border-radius:6px;background:#6366f1;color:#fff;font-weight:600;cursor:pointer}
 .err{color:#f87171;font-size:.8rem;margin-top:.75rem;min-height:1rem}
 .sso{display:block;margin-top:.5rem;padding:.65rem;border-radius:6px;background:#e2e8f0;color:#0f172a;
   font-weight:600;text-align:center;text-decoration:none}
 .or{display:flex;align-items:center;gap:.6rem;margin:1.1rem 0 .2rem;color:#64748b;font-size:.75rem}
 .or::before,.or::after{content:"";flex:1;height:1px;background:#334155}
 .hint{color:#94a3b8;font-size:.75rem;line-height:1.35;margin:.4rem 0 0}
 .ssoerr{background:#7f1d1d;color:#fecaca;border-radius:6px;padding:.6rem .7rem;font-size:.78rem;line-height:1.35;margin:.9rem 0 0}
 details{margin-top:1.1rem}
 summary{color:#94a3b8;font-size:.8rem;cursor:pointer}
 code{font-size:.72rem;color:#cbd5e1}
</style></head><body>
<form id="f" autocomplete="on">
 <h1>Sign in to Leoflow</h1>
{{ if .SSOFailed }} <p class="ssoerr" role="alert">Leoflow could not complete the sign-in on its side, so you are
 not signed in. Nothing you did is wrong and nothing is misconfigured for you: try again. If it keeps
 happening, tell whoever administers this Leoflow, the error is in the control plane's server log.</p>
{{ end }}{{ if .SSORefused }} <p class="ssoerr" role="alert">Single sign-on did not complete, so you are not signed in.
 Try again. If it keeps failing, ask whoever administers this Leoflow: the reason is recorded in
 the control plane's server log and audit trail, and is deliberately not shown here.</p>
{{ end }}{{ if .SSO }} <a class="sso" href="/api/v2/auth/oidc/login?next={{ .NextQuery }}">Sign in with single sign-on</a>
{{ end }}
{{ if .Collapse }} <details>
 <summary>Break-glass sign-in</summary>
 <p class="hint">No break-glass accounts are configured, so no password is accepted here.
 An operator can allow one by adding its address to <code>auth.oidc.break_glass_emails</code>
 (Helm: <code>auth.oidc.breakGlassEmails</code>) and restarting the control plane.</p>
{{ else }}{{ if .SSO }} <div class="or"><span>or</span></div>
 <p class="hint">The form below is for break-glass accounts. If your organization uses
 single sign-on, use the button above.</p>
{{ end }}{{ end }}
 <label for="u">Username</label><input id="u" name="username" autocomplete="username"{{ if .Focus }} autofocus{{ end }}>
 <label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
 <button type="submit">Sign in</button>
 <div class="err" id="e"></div>
{{ if .Collapse }} </details>
{{ end }}</form>
<script>
 const next = {{ .Next }};
 document.getElementById('f').addEventListener('submit', async (ev) => {
   ev.preventDefault();
   document.getElementById('e').textContent = '';
   try {
     const r = await fetch('/auth/token', {
       method: 'POST', headers: {'Content-Type':'application/json'},
       body: JSON.stringify({username: f.username.value, password: f.password.value})
     });
     if (!r.ok) {
       document.getElementById('e').textContent = r.status === 429
         ? 'Too many attempts. Wait about a minute, then try again.'
         : 'Invalid credentials';
       return;
     }
     const data = await r.json();
     const secure = location.protocol === 'https:' ? '; secure' : '';
     document.cookie = '_token=' + data.access_token + '; path=/; samesite=lax' + secure;
     // Ask the browser to remember the credentials. A fetch-based login (no
     // native form navigation) does not trigger the "save password?" prompt on
     // its own; the Credential Management API does. Best-effort, guarded.
     if (window.PasswordCredential) {
       try {
         await navigator.credentials.store(new PasswordCredential({
           id: f.username.value, name: f.username.value, password: f.password.value
         }));
       } catch (_) { /* unsupported or denied: fall through */ }
     }
     window.location.replace(next);
   } catch (_) { document.getElementById('e').textContent = 'Sign-in failed'; }
 });
</script></body></html>`))

// sanitizeNext keeps the post-login redirect on this origin: a single-slash
// absolute path only, defaulting to "/". This blocks open redirects (e.g.
// "//evil.com" or "https://evil.com").
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// loginPageHandler implements GET /api/v2/auth/login: it serves the login page
// (the Airflow UI redirects here when unauthenticated).
//
// sso says whether an OIDC flow was discovered at boot, which is the same
// condition the router uses to register /api/v2/auth/oidc/login: advertising the
// link without it would 404 the user (#1160).
//
// breakGlass says whether any address is allowed to use the password form while
// SSO is on. Under provider: oidc with an empty allowlist the gate admits NOBODY
// (newBreakGlass), so presenting the form as the primary control, with the focus
// in it, offers a way in that cannot work and answers every attempt with
// "Invalid credentials" - the same answer a wrong password gets. The form stays
// in the page, so adding an account needs no release, but it collapses and says
// which setting turns it on.
func loginPageHandler(sso, breakGlass bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		next := sanitizeNext(c.Query("next"))
		// Next is embedded as a JS string literal. NextQuery goes into an href, and
		// html/template URL-escapes a value in that context ITSELF, so escaping it
		// here too produced next=%252Fdags: the OIDC handler then saw a literal
		// "%2Fdags", sanitizeNext refused it as not absolute, and every SSO login
		// landed on the root instead of the page the user asked for. Pass the
		// sanitized path through raw and let the template do the escaping once.
		// sanitizeNext has already refused anything that is not a single-slash
		// absolute path, so neither field can carry an off-origin redirect.
		if err := loginPageTemplate.Execute(c.Writer, struct {
			Next      template.JS
			NextQuery string
			SSO       bool
			// SSORefused says the user arrived from a single sign-on this
			// deployment refused, and SSOFailed from one that broke on our side.
			// They are separate because they send the user to different places:
			// a refusal needs an administrator to change something, a failure
			// needs a retry. Both are gated on SSO, so nobody can make a
			// deployment without a flow claim a sign-on happened at all.
			SSORefused bool
			SSOFailed  bool
			// Collapse hides a form that cannot succeed; Focus puts the cursor in
			// the form only when it is a way in.
			Collapse bool
			Focus    bool
		}{
			Next:       template.JS("'" + template.JSEscapeString(next) + "'"),
			NextQuery:  next,
			SSO:        sso,
			SSORefused: sso && c.Query("sso_error") == ssoErrorRefused,
			SSOFailed:  sso && c.Query("sso_error") == ssoErrorServer,
			Collapse:   sso && !breakGlass,
			Focus:      !sso || breakGlass,
		}); err != nil {
			AbortProblem(c, http.StatusInternalServerError, "internal error", "could not render login page")
		}
	}
}

// logoutHandler implements GET /api/v2/auth/logout: it clears the _token cookie
// and returns to the login page.
func logoutHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.SetCookie(authTokenCookie, "", -1, "/", "", false, false)
		c.Redirect(http.StatusFound, "/api/v2/auth/login")
	}
}
