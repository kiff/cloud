package handlers

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/words"
)

// ─── Sign-in callback loop-breaker ───────────────────────────────
//
// Stale Clerk client/session cookies (e.g. after an idle laptop)
// can put clerk.browser.js and the hosted Account Portal into a
// redirect ping-pong:
//
//	Account Portal /sign-in?redirect_url=…/sign-in/callback
//	  → /sign-in/callback (we serve the JS bridge)
//	  → clerk.browser.js handshake finds no usable session and
//	    redirects back to the Account Portal sign-in URL
//	  → Account Portal bounces straight back to redirect_url
//	  → … forever.
//
// The bounce from the callback back to the portal is initiated
// entirely client-side by clerk.browser.js — our server returns
// 200 HTML, never a redirect — so a server-side landing counter
// is the only reliable place to detect and stop it. Past a small
// threshold we serve a static terminal page that loads NO Clerk
// JS, so the loop physically cannot continue for any user.
const (
	// callbackGuardCookie counts how many times the callback bridge
	// page has been served without a sign-in completing.
	callbackGuardCookie = "kiff_cb_attempts"

	// maxCallbackAttempts is how many times the bridge is served
	// before the loop-breaker serves the terminal page instead.
	// Headroom of 3 tolerates a legitimate Clerk handshake reload
	// before the session hydrates; a real loop trips it within a
	// few seconds.
	maxCallbackAttempts = 3

	// callbackGuardCookiePath scopes the counter to the callback
	// route so it never leaks onto the rest of the site.
	callbackGuardCookiePath = "/sign-in/callback"

	// callbackGuardTTL bounds the counter so an abandoned, maxed
	// counter never haunts a much-later visit. GET /sign-in also
	// clears it whenever a fresh sign-in begins.
	callbackGuardTTL = 5 * time.Minute
)

// readCallbackAttempts returns the current callback landing count
// from the guard cookie. Missing or malformed cookies read as 0.
func readCallbackAttempts(r *http.Request) int {
	c, err := r.Cookie(callbackGuardCookie)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(c.Value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// setCallbackAttempts writes the guard cookie with the given count.
func setCallbackAttempts(w http.ResponseWriter, n int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     callbackGuardCookie,
		Value:    strconv.Itoa(n),
		Path:     callbackGuardCookiePath,
		MaxAge:   int(callbackGuardTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCallbackAttempts expires the guard cookie. Called when a
// fresh sign-in begins (GET /sign-in) and when sign-in succeeds
// (POST /sign-in/callback), so a genuine retry always starts clean.
func clearCallbackAttempts(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     callbackGuardCookie,
		Value:    "",
		Path:     callbackGuardCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// callbackPageData feeds the inline-Clerk-bridge HTML shell.
//
// FrontendAPI is the Clerk frontend API host (e.g. "clerk.kiff.dev").
// We load Clerk's frontend SDK from the Account Portal's NPM-mirror
// path under that host. PublishableKey identifies the instance to
// the SDK. PostPath is the URL the in-page script POSTs the JWT
// to; defaults to /sign-in/callback. Next is the same-origin path
// to navigate to after a successful sign-in (defaults to /dashboard).
type callbackPageData struct {
	FrontendAPI    string
	PublishableKey string
	PostPath       string
	Next           string
	// Lang is the page's language and W its lines (callbackLanguage).
	Lang string
	W    words.SignIn
}

// callbackLanguage is the language the sign-in callback speaks: the one
// the page it returns to asked for (a tafs app's device sign-in passes
// "lang" along, components.KeepLang), else the browser's.
func callbackLanguage(r *http.Request) string {
	if next, err := url.Parse(r.URL.Query().Get("next")); err == nil {
		if l := next.Query().Get("lang"); l != "" && components.IsSiteLanguage(l) {
			return l
		}
	}
	return components.RequestLanguage(r)
}

// callbackPageTemplate is a minimal HTML shell that:
//  1. Loads clerk.browser.js from the configured frontend API.
//  2. Initializes Clerk with the publishable key.
//  3. Awaits clerk.load() and grabs a session JWT via
//     clerk.session.getToken().
//  4. POSTs {"token": "<jwt>", "next": "<next>"} as JSON to the
//     callback path on the same origin (kiff.dev), receives
//     {"redirect": "<path>"}, and navigates to it.
//
// The script is intentionally tiny. There is no UI; the page is a
// hand-off, not a destination. We render a one-line "Signing you
// in…" string so the visitor sees something during the round
// trip. On error we surface the response message in plain text;
// the user can click "try again" back to /sign-in.
var callbackPageTemplate = template.Must(template.New("clerkCallback").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <title>{{.W.Title}}</title>
  <meta name="robots" content="noindex">
  <style>
    body { font: 14px/1.4 system-ui, sans-serif; padding: 2rem; color: #ddd; background: #111111; }
    .err { color: #f88; margin-top: 1rem; }
    a { color: #9cf; }
  </style>
</head>
<body>
  <p id="status">{{.W.Signing}}</p>
  <p id="err" class="err" hidden></p>
  <p id="retry" hidden><a href="/sign-in">{{.W.TryAgain}}</a></p>
  <script
    src="https://{{.FrontendAPI}}/npm/@clerk/clerk-js@latest/dist/clerk.browser.js"
    data-clerk-publishable-key="{{.PublishableKey}}"
    crossorigin="anonymous"
    async></script>
  <script>
    (async function () {
      var statusEl = document.getElementById("status");
      var errEl = document.getElementById("err");
      var retryEl = document.getElementById("retry");
      function fail(msg) {
        statusEl.hidden = true;
        errEl.textContent = {{.W.Failed}} + " " + msg;
        errEl.hidden = false;
        retryEl.hidden = false;
      }
      // Wait up to 8 s for the Clerk global to appear.
      var waited = 0;
      while (typeof window.Clerk === "undefined" && waited < 8000) {
        await new Promise(function (r) { setTimeout(r, 50); });
        waited += 50;
      }
      if (typeof window.Clerk === "undefined") {
        return fail({{.W.NoSDK}});
      }
      try {
        await window.Clerk.load({ publishableKey: {{.PublishableKey}} });
        if (!window.Clerk.session) {
          return fail({{.W.NoSession}});
        }
        var token = await window.Clerk.session.getToken();
        if (!token) {
          return fail({{.W.NoToken}});
        }
        var postURL = {{.PostPath}};
        var nextParam = {{.Next}};
        if (nextParam) {
          postURL += (postURL.indexOf("?") === -1 ? "?" : "&") + "next=" + encodeURIComponent(nextParam);
        }
        var resp = await fetch(postURL, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "same-origin",
          body: JSON.stringify({ token: token })
        });
        var data = null;
        try { data = await resp.json(); } catch (_) { /* ignore */ }
        if (!resp.ok) {
          return fail((data && data.error) || ("HTTP " + resp.status));
        }
        var target = (data && data.redirect) || {{.Next}};
        window.location.replace(target);
      } catch (e) {
        fail((e && e.message) || String(e));
      }
    })();
  </script>
</body>
</html>
`))

// renderCallbackPage writes the callback HTML shell to w.
func renderCallbackPage(w http.ResponseWriter, data callbackPageData) error {
	if data.PostPath == "" {
		data.PostPath = "/sign-in/callback"
	}
	if data.Next == "" {
		data.Next = "/dashboard"
	}
	if data.Lang == "" {
		data.Lang = "en"
	}
	data.W = words.SignInIn(data.Lang)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return callbackPageTemplate.Execute(w, data)
}

// loopBreakPageTemplate is the terminal page the loop-breaker
// serves once the callback has been hit maxCallbackAttempts times
// without completing. It deliberately loads NO Clerk JS and issues
// NO redirect: it is a hard stop for the Account-Portal ↔ callback
// ping-pong. The only way forward is the user clicking "Try again",
// which routes through GET /sign-in and resets the counter.
var loopBreakPageTemplate = template.Must(template.New("clerkLoopBreak").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <title>{{.W.LoopTitle}}</title>
  <meta name="robots" content="noindex">
  <style>
    body { font: 15px/1.5 system-ui, sans-serif; padding: 2.5rem; max-width: 34rem; margin: 0 auto; color: #ddd; background: #111111; }
    h1 { font-size: 1.25rem; }
    a.btn { display: inline-block; margin-top: 1rem; padding: .55rem 1rem; background: #2b6; color: #003322; border-radius: 6px; text-decoration: none; font-weight: 600; }
    .hint { margin-top: 1.5rem; color: #999999; font-size: 13px; }
    code { background: #222222; padding: .1rem .3rem; border-radius: 4px; }
  </style>
</head>
<body>
  <h1>{{.W.LoopHead}}</h1>
  <p>{{.W.LoopBody}}</p>
  <p><a class="btn" href="/sign-in">{{.W.LoopButton}}</a></p>
  <p class="hint">
    {{.W.LoopHintBefore}}
    (<code>kiff.dev</code>, <code>accounts.kiff.dev</code>,
    <code>clerk.kiff.dev</code>) {{.W.LoopHintAfter}}
  </p>
</body>
</html>
`))

// renderLoopBreakPage writes the terminal loop-breaker page to w, in lang.
func renderLoopBreakPage(w http.ResponseWriter, lang string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return loopBreakPageTemplate.Execute(w, struct {
		Lang string
		W    words.SignIn
	}{lang, words.SignInIn(lang)})
}

// isSafeNext returns true when the given string is a same-origin
// path: starts with "/", no scheme, no host. Anything else is
// rejected as a possible open-redirect vector.
func isSafeNext(next string) bool {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") {
		return false
	}
	// "//evil.example/x" is host-relative protocol-relative; reject.
	if strings.HasPrefix(next, "//") {
		return false
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return false
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return false
	}
	// Browsers read a backslash as a slash, so "/\evil.example", or
	// "/%5Cevil.example" once decoded, leaves the site like "//". Control
	// characters are refused for the same reason: some are dropped
	// before the URL is read.
	if strings.Contains(next, "\\") || strings.Contains(parsed.Path, "\\") {
		return false
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return strings.HasPrefix(parsed.Path, "/") && !strings.HasPrefix(parsed.Path, "//")
}

// writeJSON marshals v to w with the given status code. Errors
// fall back to a plain "internal error" body.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Fallback already past WriteHeader; nothing useful left
		// to do but log via the caller's path. Best-effort.
		_, _ = w.Write([]byte(`{"error":"encoding failure"}`))
	}
}
