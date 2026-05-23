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
</style></head><body>
<form id="f" autocomplete="on">
 <h1>Sign in to Leoflow</h1>
 <label for="u">Username</label><input id="u" name="username" autocomplete="username" autofocus>
 <label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
 <button type="submit">Sign in</button>
 <div class="err" id="e"></div>
</form>
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
     if (!r.ok) { document.getElementById('e').textContent = 'Invalid credentials'; return; }
     const data = await r.json();
     const secure = location.protocol === 'https:' ? '; secure' : '';
     document.cookie = '_token=' + data.access_token + '; path=/; samesite=lax' + secure;
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
func loginPageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		// template/html escapes Next for safe embedding as a JS string literal.
		if err := loginPageTemplate.Execute(c.Writer, struct{ Next template.JS }{
			Next: template.JS("'" + template.JSEscapeString(sanitizeNext(c.Query("next"))) + "'"),
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
