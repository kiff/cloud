package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/session"
	"github.com/kiff/cloud/web/internal/words"
)

// The tafs apps' sign-in page (/sign-in/app, apps/api's appsignin): the page
// the app's sheet opens on once /oauth/authorize has kept its request. It
// signs the person in on this page, in the app's language, then answers the
// request through the API, which sends the sheet back to the app with a code.
//
// It never answers for the person without them acting (RFC 8252 §8.6: another
// app can claim a private-use redirect). A person with no KIFF session signs
// in on the form, and only a sign-in made for this request, in a browser that
// was shown the form for it, comes back to an approve: a cookie bound to the
// request, used once, and the session's own SignedInFor, both checked here
// before the API is called. The API's recent sign-in check holds the rest. A
// person signed in any other way is asked, "Continue as …", never approved
// automatically.
const (
	appSignInPath = "/sign-in/app"
	// appSignInCookie marks that this browser was shown the form for one
	// request: "<request>.<unix seconds>", sealed with the session secret
	// (session.Manager.Seal), so it can't be forged or carried over.
	appSignInCookie = "kiff_app_signin"
	// appSignInSeal is the mark's purpose for Seal.
	appSignInSeal = "app-signin-form"
	// appSignInFormTTL is how long the form's mark holds: a request lives as
	// long (apps/api's appsignin.RequestTTL).
	appSignInFormTTL = 10 * time.Minute
	// appRedirectPrefix is where the API may send the sheet: the app's own
	// redirect (appsignin.RedirectURI), nowhere else.
	appRedirectPrefix = appScheme + "/oauth?"
	// appScheme is the apps' return scheme, the same on every platform. The
	// page's form-action allows it, and only it, beside the page itself:
	// "Continue as" is a form whose answer redirects to the app, and a
	// browser holds a form's redirect to form-action too.
	appScheme = "dev.kiff.tafs:"
	// appBackToApp is where "Back to the app" sends the sheet when there's
	// nothing left to do here: the app's own redirect, as a cancel. The
	// request is spent or unknown, so there's no state to send; the app
	// takes it as "signing in didn't go on" and lets the person start again.
	appBackToApp = template.URL("dev.kiff.tafs:/oauth?error=access_denied")
	// providerScriptVersion pins the sign-in provider's browser script the
	// form loads: a new version is a reviewed change, never @latest.
	providerScriptVersion = "6.46.2"
	// providerUIVersion pins the provider's UI script, released with
	// providerScriptVersion: since its version 6 the core script carries no
	// components, so without this the form mounts nothing. Bump the two together.
	providerUIVersion = "1.44.2"
)

// SignInScript is the sign-in provider's browser script and key, for the
// form. Without one (the stub provider), the form is /sign-in's.
type SignInScript interface {
	FrontendAPI() string
	PublishableKey() string
}

// AppSignInHandler serves /sign-in/app. Off (KIFF_APP_SIGNIN, as the API's),
// the page says signing in inside the app isn't on, and does nothing else.
type AppSignInHandler struct {
	on       bool
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	script   SignInScript
	logger   *slog.Logger
	now      func() time.Time
	// code is signing in with a code sent by email: its link sits under
	// the form when it's on.
	code *CodeSignInHandler
}

// WithCodeSignIn shows the link to a code under the form when code is on.
func (h *AppSignInHandler) WithCodeSignIn(code *CodeSignInHandler) *AppSignInHandler {
	h.code = code
	return h
}

// NewAppSignInHandler wires the page; on is KIFF_APP_SIGNIN. provider's
// script is used when it has one.
func NewAppSignInHandler(on bool, api *apiclient.Client, sessions *session.Manager, bearers BearerSource, provider auth.Provider, logger *slog.Logger) *AppSignInHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &AppSignInHandler{on: on, api: api, sessions: sessions, bearers: bearers, logger: logger, now: time.Now}
	if s, ok := provider.(SignInScript); ok && s.FrontendAPI() != "" && s.PublishableKey() != "" {
		h.script = s
	}
	return h
}

// Register mounts the page and its two answers.
func (h *AppSignInHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+appSignInPath, h.show)
	mux.HandleFunc("POST "+appSignInPath, h.answer)
}

// appSignInView is what the page shows.
type appSignInView struct {
	Lang    string
	W       words.AppSignIn
	Request string
	// Form: the sign-in form, with the provider's script and key, the email
	// to prefill, and where it goes once signed in.
	Form                        bool
	FrontendAPI, PublishableKey string
	Hint, Done                  string
	// Email: "Continue as" this person.
	Email string
	// Other is where "Use another account" goes.
	Other string
	// Stopped is why there's nothing to do here but go back to the app, and
	// Back is the button that does.
	Stopped string
	Back    template.URL
	// Nonce lets the page's own script run under its CSP; ScriptVersion and
	// UIVersion are the provider's core and UI scripts' pinned versions.
	Nonce, ScriptVersion, UIVersion string
	// CodeLink is signing in with a code sent by email, for this request,
	// and CodeWords its link's words; empty when it's off.
	CodeLink, CodeWords string
}

func (h *AppSignInHandler) show(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	request := strings.TrimSpace(q.Get("request"))
	lang := appSignInLang(r)
	v := appSignInView{Lang: lang, W: words.AppSignInIn(lang), Request: request}
	if !h.on {
		v.Stopped = v.W.Off
		h.render(w, r, http.StatusNotFound, v)
		return
	}
	if !requestShaped(request) {
		v.Stopped = v.W.Expired
		h.render(w, r, http.StatusBadRequest, v)
		return
	}
	s, err := h.sessions.Read(r)
	if err != nil {
		h.showForm(w, r, v, q.Get("login_hint"))
		return
	}
	// Back from the form shown for this request, signed in for it: answer it
	// now, once.
	if h.takeFormMark(w, r, request) && s.SignedInFor == request {
		h.approve(w, r, s, v)
		return
	}
	v.Email = s.Email
	v.Other = "/sign-out?next=" + url.QueryEscape(h.back(request, lang))
	h.render(w, r, http.StatusOK, v)
}

// answer is the person's "Continue as" or "Cancel": a tap on this page, so a
// POST from anywhere else is refused (the server's SameOriginOnly does too).
func (h *AppSignInHandler) answer(w http.ResponseWriter, r *http.Request) {
	if !fromThisPage(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	request := strings.TrimSpace(r.PostFormValue("request"))
	lang := appSignInLang(r)
	v := appSignInView{Lang: lang, W: words.AppSignInIn(lang), Request: request}
	if !h.on {
		v.Stopped = v.W.Off
		h.render(w, r, http.StatusNotFound, v)
		return
	}
	if !requestShaped(request) {
		v.Stopped = v.W.Expired
		h.render(w, r, http.StatusBadRequest, v)
		return
	}
	s, err := h.sessions.Read(r)
	if err != nil {
		http.Redirect(w, r, h.back(request, lang), http.StatusSeeOther)
		return
	}
	if r.PostFormValue("answer") == "cancel" {
		h.deny(w, r, s, v)
		return
	}
	h.approve(w, r, s, v)
}

// showForm shows the sign-in form and marks this browser as shown it for the
// request. A provider session left in the browser is signed out before the
// form mounts, so nothing passes without the person signing in here.
func (h *AppSignInHandler) showForm(w http.ResponseWriter, r *http.Request, v appSignInView, hint string) {
	http.SetCookie(w, &http.Cookie{
		Name: appSignInCookie, Value: h.sessions.Seal(appSignInSeal, v.Request+"."+strconv.FormatInt(h.now().Unix(), 10)),
		Path: appSignInPath, MaxAge: int(appSignInFormTTL.Seconds()),
		HttpOnly: true, Secure: h.sessions.CookieSecure(), SameSite: http.SameSiteLaxMode,
	})
	back := h.back(v.Request, v.Lang)
	if h.script == nil {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(back), http.StatusFound)
		return
	}
	v.Form = true
	v.FrontendAPI, v.PublishableKey, v.ScriptVersion, v.UIVersion = h.script.FrontendAPI(), h.script.PublishableKey(), providerScriptVersion, providerUIVersion
	v.Hint = hint
	if link := h.code.CodeLink(back, v.Lang); link != "" {
		v.CodeLink, v.CodeWords = link, words.CodeSignInIn(v.Lang).Link
	}
	// The provider sends the browser to the callback, which mints KIFF's
	// session and comes back here.
	v.Done = "/sign-in/callback?next=" + url.QueryEscape(back)
	h.render(w, r, http.StatusOK, v)
}

// takeFormMark reports whether this browser was shown the form for request,
// recently, and clears the mark either way: it's used once.
func (h *AppSignInHandler) takeFormMark(w http.ResponseWriter, r *http.Request, request string) bool {
	c, err := r.Cookie(appSignInCookie)
	if err != nil {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: appSignInCookie, Value: "", Path: appSignInPath, MaxAge: -1,
		HttpOnly: true, Secure: h.sessions.CookieSecure(), SameSite: http.SameSiteLaxMode})
	mark, ok := h.sessions.Open(appSignInSeal, c.Value)
	if !ok {
		return false
	}
	id, at, ok := strings.Cut(mark, ".")
	if !ok || id != request {
		return false
	}
	sec, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return false
	}
	shown := time.Unix(sec, 0)
	return !shown.After(h.now()) && h.now().Sub(shown) < appSignInFormTTL
}

func (h *AppSignInHandler) approve(w http.ResponseWriter, r *http.Request, s session.Session, v appSignInView) {
	h.answerWith(w, r, s, v, h.api.ApproveAppSignIn)
}

func (h *AppSignInHandler) deny(w http.ResponseWriter, r *http.Request, s session.Session, v appSignInView) {
	h.answerWith(w, r, s, v, h.api.DenyAppSignIn)
}

// answerWith answers the request through the API and sends the sheet where
// it says, which can only be the app's own redirect.
func (h *AppSignInHandler) answerWith(w http.ResponseWriter, r *http.Request, s session.Session, v appSignInView,
	call func(ctx context.Context, bearer, request string) (string, error)) {
	if h.api == nil {
		v.Stopped = v.W.Failed
		h.render(w, r, http.StatusServiceUnavailable, v)
		return
	}
	bearer, err := h.bearerFor(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, h.back(v.Request, v.Lang), http.StatusSeeOther)
		return
	}
	to, err := call(r.Context(), bearer, v.Request)
	switch {
	case err == nil && strings.HasPrefix(to, appRedirectPrefix):
		http.Redirect(w, r, to, http.StatusSeeOther)
	case err == nil:
		// Where, never the whole address: it may carry a code.
		where := "unparsable"
		if u, perr := url.Parse(to); perr == nil {
			where = u.Scheme + "://" + u.Host
		}
		h.logger.ErrorContext(r.Context(), "app sign-in: the API sent the sheet elsewhere", slog.String("to", where))
		v.Stopped = v.W.Failed
		h.render(w, r, http.StatusBadGateway, v)
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, h.back(v.Request, v.Lang), http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrReauthRequired):
		// An older sign-in: sign in again on the form, then come back. The
		// API kept the request.
		http.Redirect(w, r, "/sign-out?next="+url.QueryEscape(h.back(v.Request, v.Lang)), http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrAppSignInGone):
		v.Stopped = v.W.Expired
		h.render(w, r, http.StatusGone, v)
	case errors.Is(err, apiclient.ErrAppSignInOff):
		v.Stopped = v.W.Off
		h.render(w, r, http.StatusNotFound, v)
	default:
		h.logger.WarnContext(r.Context(), "app sign-in: answer failed", slog.String("err", err.Error()))
		v.Stopped = v.W.Failed
		h.render(w, r, http.StatusBadGateway, v)
	}
}

func (h *AppSignInHandler) bearerFor(r *http.Request, s session.Session) (string, error) {
	if h.bearers != nil {
		return h.bearers.BearerFor(r.Context(), s)
	}
	return s.Bearer, nil
}

// back is this page again for request, in lang: where sign-in, sign-out and
// "Use another account" come back to.
func (h *AppSignInHandler) back(request, lang string) string {
	q := url.Values{"request": {request}}
	if lang != "en" {
		q.Set("ui_locales", lang)
	}
	return appSignInPath + "?" + q.Encode()
}

// appSignInRequestOf is the app sign-in request a sign-in's next names
// (/sign-in/app?request=…), or "": the session it mints is signed in for it.
func appSignInRequestOf(next string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path != appSignInPath {
		return ""
	}
	if req := u.Query().Get("request"); requestShaped(req) {
		return req
	}
	return ""
}

// fromThisPage reports whether a POST came from this site: Sec-Fetch-Site
// where the browser sends it, else Origin, else (an old browser) yes, with
// the server's SameOriginOnly and the Lax session cookie behind it.
func fromThisPage(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return true
}

// appSignInLang is the app's language: ui_locales when it's one of the
// site's (apps/api passes only those), else the request's.
func appSignInLang(r *http.Request) string {
	for _, l := range []string{r.URL.Query().Get("ui_locales"), r.PostFormValue("ui_locales")} {
		if l = strings.TrimSpace(l); l != "" && components.IsSiteLanguage(l) {
			return l
		}
	}
	return components.RequestLanguage(r)
}

// requestShaped reports whether s looks like apps/api's request id: URL-safe,
// of a sane length. The API decides whether it's one.
func requestShaped(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (h *AppSignInHandler) render(w http.ResponseWriter, r *http.Request, status int, v appSignInView) {
	v.Nonce = newNonce()
	if v.Stopped != "" {
		v.Back = appBackToApp
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Never framed: a site can't lay "Continue as …" under its own bait.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", appSignInCSP(v.FrontendAPI, v.Nonce))
	w.WriteHeader(status)
	if err := appSignInPage.Execute(w, v); err != nil {
		h.logger.ErrorContext(r.Context(), "app sign-in: render", slog.String("err", err.Error()))
	}
}

// appSignInCSP is the page's policy: its own script by nonce, the provider's
// script, requests and pictures from its frontend host and the hosts its
// bot and fraud checks use (the provider's documented list), no framing, and
// forms that go only to this site or, by its redirect, back to the app
// (appScheme). The server sends a form's answer to the app only at
// appRedirectPrefix.
func appSignInCSP(frontendAPI, nonce string) string {
	script, connect := "'self' 'nonce-"+nonce+"'", "'self'"
	if frontendAPI != "" {
		fapi := "https://" + frontendAPI
		script += " " + fapi + " https://challenges.cloudflare.com https://*.protect.clerk.com"
		connect += " " + fapi + " https://*.protect.clerk.com:*"
	}
	return "default-src 'none'; script-src " + script + "; connect-src " + connect +
		"; img-src 'self' https://img.clerk.com; style-src 'self' 'unsafe-inline'; font-src 'self'" +
		"; frame-src https://challenges.cloudflare.com https://*.protect.clerk.com; worker-src 'self' blob:" +
		"; frame-ancestors 'none'; base-uri 'none'; form-action 'self' " + appScheme
}

// newNonce is a fresh CSP nonce, one per page.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// URL-safe: no character html/template would write as an entity.
	return base64.RawURLEncoding.EncodeToString(b)
}

var appSignInPage = template.Must(template.New("appSignIn").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>{{.W.Title}}</title>
  <style>
    :root { color-scheme: light dark; --ink: #1b1b1f; --soft: #5b5b66; --bg: #ffffff; --line: #e4e4ea; --amber: #c77d00; }
    @media (prefers-color-scheme: dark) { :root { --ink: #ececf1; --soft: #a0a0ab; --bg: #111114; --line: #2a2a31; --amber: #f0a43a; } }
    body { margin: 0; font: 16px/1.45 system-ui, -apple-system, sans-serif; color: var(--ink); background: var(--bg); }
    main { max-width: 420px; margin: 0 auto; padding: 24px 16px 40px; }
    .brand { display: flex; align-items: center; gap: 8px; font-weight: 600; margin-bottom: 24px; }
    .brand img { width: 28px; height: 28px; border-radius: 7px; }
    /* The page's own elements only: the provider's form draws inside
       #sign-in, and a bare p, form or button rule would reshape its fields
       (a full-width show-password button over the password, K5, 21 Nov). */
    main > h1 { font-size: 24px; margin: 0 0 8px; }
    main > p, #not-loaded > p { color: var(--soft); margin: 0 0 20px; }
    main > form { margin: 0 0 12px; }
    .go, .no, .other { display: block; width: 100%; box-sizing: border-box; padding: 14px; border-radius: 12px; font: inherit; font-weight: 600; text-align: center; text-decoration: none; }
    .go { background: var(--amber); color: #fff; border: 0; }
    .no { background: none; color: var(--soft); border: 0; }
    .other { color: var(--ink); border: 1px solid var(--line); margin-bottom: 12px; }
    #sign-in { min-height: 320px; }
  </style>
</head>
<body>
<main>
  <div class="brand"><img src="/static/img/tafs/tafs-icon-512.png" alt="" aria-hidden="true">tafs by KIFF</div>
  <h1>{{.W.Title}}</h1>
  {{if .Stopped}}
  <p role="alert">{{.Stopped}}</p>
  <a class="other" href="{{.Back}}">{{.W.BackToApp}}</a>
  {{else if .Form}}
  <p>{{.W.Lead}}</p>
  <div id="sign-in"></div>
  {{if .CodeLink}}<a class="other" id="email-code-link" href="{{.CodeLink}}">{{.CodeWords}}</a>{{end}}
  <div id="not-loaded" hidden>
    <p role="alert">{{.W.NotLoaded}}</p>
    <button class="go" type="button" id="try-again">{{.W.TryAgain}}</button>
  </div>
  <script
    src="https://{{.FrontendAPI}}/npm/@clerk/clerk-js@{{.ScriptVersion}}/dist/clerk.browser.js"
    data-clerk-publishable-key="{{.PublishableKey}}"
    crossorigin="anonymous"
    async></script>
  <script
    src="https://{{.FrontendAPI}}/npm/@clerk/ui@{{.UIVersion}}/dist/ui.browser.js"
    crossorigin="anonymous"
    async></script>
  <script nonce="{{.Nonce}}">
    (async function () {
      var box = document.getElementById("sign-in");
      // Never a blank page: when the form can't load or mounts nothing to
      // sign in with, the person reads why and can load it again.
      function notLoaded() {
        box.hidden = true;
        document.getElementById("not-loaded").hidden = false;
      }
      document.getElementById("try-again").addEventListener("click", function () { window.location.reload(); });
      try {
        var waited = 0;
        while ((typeof window.Clerk === "undefined" || typeof window.__internal_ClerkUICtor === "undefined") && waited < 15000) {
          await new Promise(function (r) { setTimeout(r, 50); });
          waited += 50;
        }
        if (typeof window.Clerk === "undefined" || typeof window.__internal_ClerkUICtor === "undefined") return notLoaded();
        await window.Clerk.load({ publishableKey: {{.PublishableKey}}, ui: { ClerkUI: window.__internal_ClerkUICtor } });
        // A provider session left in this browser is never used: the person
        // signs in here.
        if (window.Clerk.session) await window.Clerk.signOut();
        var done = {{.Done}};
        var hint = {{.Hint}};
        window.Clerk.mountSignIn(box, {
          withSignUp: true,
          forceRedirectUrl: done,
          signUpForceRedirectUrl: done,
          initialValues: hint ? { emailAddress: hint } : undefined
        });
        setTimeout(function () { if (!box.querySelector("button, input")) notLoaded(); }, 10000);
      } catch (e) {
        notLoaded();
      }
    })();
  </script>
  {{else}}
  <form method="post" action="/sign-in/app">
    <input type="hidden" name="request" value="{{.Request}}">
    <input type="hidden" name="ui_locales" value="{{.Lang}}">
    <button class="go" type="submit" name="answer" value="continue">{{.W.ContinueAs .Email}}</button>
  </form>
  <a class="other" href="{{.Other}}">{{.W.OtherAccount}}</a>
  <form method="post" action="/sign-in/app">
    <input type="hidden" name="request" value="{{.Request}}">
    <input type="hidden" name="ui_locales" value="{{.Lang}}">
    <button class="no" type="submit" name="answer" value="cancel">{{.W.Cancel}}</button>
  </form>
  {{end}}
</main>
</body>
</html>
`))
