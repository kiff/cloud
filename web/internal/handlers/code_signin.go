package handlers

import (
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
	"github.com/kiff/cloud/web/internal/region"
	"github.com/kiff/cloud/web/internal/session"
	"github.com/kiff/cloud/web/internal/words"
)

// Signing in with a code sent by email (apps/api's codesignin, README): a
// person who turned it on asks for a code on /sign-in/code and types it
// there. KIFF checks it and hands this page the sign-in provider's one-time
// sign-in token; the page completes the provider's sign-in with it and the
// callback mints KIFF's session, as every sign-in does.
//
// A code works only in the browser that asked for it: the ask sets a sealed
// cookie with a fresh flow id, and KIFF keeps the code against that id.
// Asking answers the same for every address. Off (KIFF_SIGNIN_CODE), the
// link isn't shown and the routes say it isn't on.
const (
	codeSignInPath = "/sign-in/code"
	// codeFlowCookie is "<flow>\x00<unix seconds>\x00<email>", sealed.
	codeFlowCookie = "kiff_code_flow"
	codeFlowSeal   = "code-signin-flow"
	// codeFlowTTL is a code's life (codesignin.CodeTTL).
	codeFlowTTL = 10 * time.Minute
)

// CodeSignInHandler serves /sign-in/code and, when on, the web's /sign-in
// page with the provider's form and the link to it.
type CodeSignInHandler struct {
	on       bool
	api      *apiclient.Client
	sessions *session.Manager
	script   SignInScript
	logger   *slog.Logger
	now      func() time.Time
	// country is the caller's country for the email (the region gate's),
	// or nil.
	country func(*http.Request) string
}

// NewCodeSignInHandler wires the pages; on is KIFF_SIGNIN_CODE.
func NewCodeSignInHandler(on bool, api *apiclient.Client, sessions *session.Manager, provider auth.Provider, country func(*http.Request) string, logger *slog.Logger) *CodeSignInHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &CodeSignInHandler{on: on, api: api, sessions: sessions, logger: logger, now: time.Now, country: country}
	if s, ok := provider.(SignInScript); ok && s.FrontendAPI() != "" && s.PublishableKey() != "" {
		h.script = s
	}
	return h
}

// On reports whether the link and pages are on.
func (h *CodeSignInHandler) On() bool { return h != nil && h.on }

// Register mounts the pages.
func (h *CodeSignInHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+codeSignInPath, h.show)
	mux.HandleFunc("POST "+codeSignInPath, h.send)
	mux.HandleFunc("POST "+codeSignInPath+"/verify", h.verify)
	// A reload or a bookmark of the code's page goes back to asking.
	mux.HandleFunc("GET "+codeSignInPath+"/verify", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, codeSignInPath+"?next="+url.QueryEscape(codeNext(r)), http.StatusSeeOther)
	})
}

type codeView struct {
	Lang  string
	W     words.CodeSignIn
	Title string
	// Step is "email", "code", "finish", "signin" or "off".
	Step    string
	Next    string
	Email   string
	Problem string
	// Other is "Sign in another way"; Link is the code page from /sign-in.
	Other, Link string
	// For "finish" and "signin": the provider's script and key, the
	// token, where to go once signed in.
	FrontendAPI, PublishableKey, Ticket, Done string
	Nonce, ScriptVersion, UIVersion           string
}

// codeNext is where a sign-in by code returns: a same-site path, the app
// sheet's page included, else the dashboard.
func codeNext(r *http.Request) string {
	next := strings.TrimSpace(r.URL.Query().Get("next"))
	if r.Method == http.MethodPost {
		next = strings.TrimSpace(r.PostFormValue("next"))
	}
	if !isSafeNext(next) {
		return "/dashboard"
	}
	return next
}

func (h *CodeSignInHandler) view(r *http.Request, step string) codeView {
	lang := appSignInLang(r)
	next := codeNext(r)
	v := codeView{Lang: lang, W: words.CodeSignInIn(lang), Step: step, Next: next}
	v.Title = v.W.Title
	v.Other = "/sign-in?next=" + url.QueryEscape(next)
	if appSignInRequestOf(next) != "" {
		// From the app's sheet: back to its page, which shows the form.
		v.Other = next
	}
	return v
}

func (h *CodeSignInHandler) show(w http.ResponseWriter, r *http.Request) {
	if !h.on {
		h.render(w, r, http.StatusNotFound, h.view(r, "off"))
		return
	}
	h.render(w, r, http.StatusOK, h.view(r, "email"))
}

// send asks KIFF for a code. The page says the same whatever the address:
// "if signing in with a code is on for it, a code is on its way".
func (h *CodeSignInHandler) send(w http.ResponseWriter, r *http.Request) {
	if !fromThisPage(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v := h.view(r, "email")
	if !h.on || h.api == nil {
		v.Step = "off"
		h.render(w, r, http.StatusNotFound, v)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	if email == "" || len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsAny(email, " <>\"\x00") {
		v.Problem = v.W.Wrong
		h.render(w, r, http.StatusBadRequest, v)
		return
	}
	flow := newFlowID()
	http.SetCookie(w, &http.Cookie{
		Name: codeFlowCookie, Value: h.sessions.Seal(codeFlowSeal, flow+"\x00"+strconv.FormatInt(h.now().Unix(), 10)+"\x00"+email),
		Path: codeSignInPath, MaxAge: int(codeFlowTTL.Seconds()),
		HttpOnly: true, Secure: h.sessions.CookieSecure(), SameSite: http.SameSiteLaxMode,
	})
	ask := apiclient.CodeAsk{Email: email, Flow: flow, IP: clientIP(r), Lang: v.Lang, Browser: browserOf(r.UserAgent())}
	if h.country != nil {
		ask.Country = h.country(r)
	}
	err := h.api.SendSignInCode(r.Context(), ask)
	switch {
	case err == nil:
	case errors.Is(err, apiclient.ErrCodeOff):
		v.Step = "off"
		h.render(w, r, http.StatusNotFound, v)
		return
	default:
		// The address is never logged.
		h.logger.WarnContext(r.Context(), "code sign-in: send failed", slog.String("err", err.Error()))
		v.Problem = v.W.Unavailable
		h.render(w, r, http.StatusBadGateway, v)
		return
	}
	v.Step, v.Email = "code", email
	h.render(w, r, http.StatusOK, v)
}

// flow is this browser's ask: its id and address, if it asked recently.
func (h *CodeSignInHandler) flow(r *http.Request) (id, email string, ok bool) {
	c, err := r.Cookie(codeFlowCookie)
	if err != nil {
		return "", "", false
	}
	mark, ok := h.sessions.Open(codeFlowSeal, c.Value)
	if !ok {
		return "", "", false
	}
	parts := strings.SplitN(mark, "\x00", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	sec, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", "", false
	}
	asked := time.Unix(sec, 0)
	if asked.After(h.now()) || h.now().Sub(asked) >= codeFlowTTL {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// verify checks the code with KIFF and, on a match, completes the sign-in.
func (h *CodeSignInHandler) verify(w http.ResponseWriter, r *http.Request) {
	if !fromThisPage(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v := h.view(r, "code")
	if !h.on || h.api == nil {
		v.Step = "off"
		h.render(w, r, http.StatusNotFound, v)
		return
	}
	flow, email, ok := h.flow(r)
	if !ok {
		v.Step, v.Problem = "email", v.W.Lapsed
		h.render(w, r, http.StatusGone, v)
		return
	}
	v.Email = email
	ticket, err := h.api.VerifySignInCode(r.Context(), email, flow, r.PostFormValue("code"), clientIP(r))
	switch {
	case err == nil:
	case errors.Is(err, apiclient.ErrCodeWrong):
		v.Problem = v.W.Wrong
		h.render(w, r, http.StatusBadRequest, v)
		return
	case errors.Is(err, apiclient.ErrCodeSlowDown):
		v.Problem = v.W.SlowDown
		h.render(w, r, http.StatusTooManyRequests, v)
		return
	case errors.Is(err, apiclient.ErrCodeSecondFactor):
		v.Step, v.Problem = "email", v.W.SecondFactor
		h.render(w, r, http.StatusForbidden, v)
		return
	case errors.Is(err, apiclient.ErrCodeOff):
		v.Step = "off"
		h.render(w, r, http.StatusNotFound, v)
		return
	default:
		h.logger.WarnContext(r.Context(), "code sign-in: verify failed", slog.String("err", err.Error()))
		v.Problem = v.W.Unavailable
		h.render(w, r, http.StatusBadGateway, v)
		return
	}
	// Used: the ask is spent.
	http.SetCookie(w, &http.Cookie{Name: codeFlowCookie, Value: "", Path: codeSignInPath, MaxAge: -1,
		HttpOnly: true, Secure: h.sessions.CookieSecure(), SameSite: http.SameSiteLaxMode})
	if h.script == nil {
		v.Problem = v.W.Unavailable
		h.render(w, r, http.StatusServiceUnavailable, v)
		return
	}
	v.Step, v.Title, v.Ticket = "finish", v.W.Finishing, ticket
	v.FrontendAPI, v.PublishableKey, v.ScriptVersion = h.script.FrontendAPI(), h.script.PublishableKey(), providerScriptVersion
	v.Done = "/sign-in/callback?next=" + url.QueryEscape(v.Next)
	h.render(w, r, http.StatusOK, v)
}

// SignInPage is the web's /sign-in when code sign-in is on: the provider's
// form on KIFF's page, as the app's sheet has it, with the link to a code
// under it. Without the provider's script it isn't served: false.
func (h *CodeSignInHandler) SignInPage(w http.ResponseWriter, r *http.Request) bool {
	if !h.On() || h.script == nil {
		return false
	}
	next := redirectTarget(r)
	lang := appSignInLang(r)
	v := codeView{Lang: lang, W: words.CodeSignInIn(lang), Step: "signin", Next: next}
	v.Title = words.AppSignInIn(lang).Title
	v.Title = strings.Replace(v.Title, "tafs", "KIFF", 1)
	v.Link = codeSignInPath + "?next=" + url.QueryEscape(next)
	v.FrontendAPI, v.PublishableKey, v.ScriptVersion, v.UIVersion = h.script.FrontendAPI(), h.script.PublishableKey(), providerScriptVersion, providerUIVersion
	v.Done = "/sign-in/callback?next=" + url.QueryEscape(next)
	h.render(w, r, http.StatusOK, v)
	return true
}

// CodeLink is the code page for the app sheet's request, or "" when off.
func (h *CodeSignInHandler) CodeLink(next, lang string) string {
	if !h.On() {
		return ""
	}
	q := url.Values{"next": {next}}
	if lang != "en" {
		q.Set("ui_locales", lang)
	}
	return codeSignInPath + "?" + q.Encode()
}

func (h *CodeSignInHandler) render(w http.ResponseWriter, r *http.Request, status int, v codeView) {
	v.Nonce = newNonce()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	csp := "default-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	if v.Step == "finish" || v.Step == "signin" {
		csp = appSignInCSP(v.FrontendAPI, v.Nonce)
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.WriteHeader(status)
	if err := codePage.Execute(w, v); err != nil {
		h.logger.ErrorContext(r.Context(), "code sign-in: render", slog.String("err", err.Error()))
	}
}

func newFlowID() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// clientIP is the caller's address as the region gate trusts it.
func clientIP(r *http.Request) string {
	addr, _ := region.ClientAddr(r)
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// browserOf names the browser for the email, coarsely, from its user agent.
func browserOf(ua string) string {
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"SamsungBrowser/", "Samsung Internet"}, {"Firefox/", "Firefox"},
		{"FxiOS/", "Firefox"}, {"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.token) {
			return b.name
		}
	}
	return ""
}

var codePage = template.Must(template.New("code").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light dark; --ink: #1b1b1f; --soft: #5b5b66; --bg: #ffffff; --line: #e4e4ea; --amber: #c77d00; --bad: #b42318; }
    @media (prefers-color-scheme: dark) { :root { --ink: #ececf1; --soft: #a0a0ab; --bg: #111114; --line: #2a2a31; --amber: #f0a43a; --bad: #f97066; } }
    body { margin: 0; font: 16px/1.45 system-ui, -apple-system, sans-serif; color: var(--ink); background: var(--bg); }
    main { max-width: 420px; margin: 0 auto; padding: 24px 16px 40px; }
    .brand { font-weight: 600; margin-bottom: 24px; }
    main > h1 { font-size: 24px; margin: 0 0 8px; }
    main > p { color: var(--soft); margin: 0 0 20px; }
    main > p.problem { color: var(--bad); }
    main > form label { display: block; font-weight: 600; margin: 0 0 6px; }
    main > form input { display: block; width: 100%; box-sizing: border-box; padding: 12px; border: 1px solid var(--line); border-radius: 10px; font: inherit; background: var(--bg); color: var(--ink); margin: 0 0 12px; }
    main > form input.code { font: 600 24px/1.2 ui-monospace, Menlo, monospace; letter-spacing: 6px; text-align: center; }
    .go, .other { display: block; width: 100%; box-sizing: border-box; padding: 14px; border-radius: 12px; font: inherit; font-weight: 600; text-align: center; text-decoration: none; margin: 0 0 12px; }
    .go { background: var(--amber); color: #fff; border: 0; }
    .other { color: var(--ink); border: 1px solid var(--line); }
    #sign-in { min-height: 320px; }
  </style>
</head>
<body>
<main>
  <div class="brand">KIFF</div>
  <h1>{{.Title}}</h1>
  {{if eq .Step "off"}}
  <p role="alert">{{.W.SettingNotAvailable}}</p>
  <a class="other" href="{{.Other}}">{{.W.OtherWays}}</a>
  {{else if eq .Step "email"}}
  {{if .Problem}}<p class="problem" role="alert">{{.Problem}}</p>{{else}}<p>{{.W.Lead}}</p>{{end}}
  <form method="post" action="/sign-in/code">
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="ui_locales" value="{{.Lang}}">
    <label for="email">{{.W.EmailLabel}}</label>
    <input id="email" name="email" type="email" autocomplete="email" required value="{{.Email}}">
    <button class="go" type="submit">{{.W.Send}}</button>
  </form>
  <a class="other" href="{{.Other}}">{{.W.OtherWays}}</a>
  {{else if eq .Step "code"}}
  {{if .Problem}}<p class="problem" role="alert">{{.Problem}}</p>{{else}}<p>{{.W.Sent .Email}}</p>{{end}}
  <form method="post" action="/sign-in/code/verify">
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="ui_locales" value="{{.Lang}}">
    <label for="code">{{.W.CodeLabel}}</label>
    <input id="code" class="code" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9 ]{6,7}" maxlength="7" required autofocus>
    <button class="go" type="submit">{{.W.Verify}}</button>
  </form>
  <form method="post" action="/sign-in/code">
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="ui_locales" value="{{.Lang}}">
    <input type="hidden" name="email" value="{{.Email}}">
    <button class="other" type="submit">{{.W.Again}}</button>
  </form>
  <a class="other" href="{{.Other}}">{{.W.OtherWays}}</a>
  {{else if eq .Step "finish"}}
  <p id="finishing">{{.W.Finishing}}</p>
  <p class="problem" id="not-finished" role="alert" hidden>{{.W.NotLoaded}}</p>
  <a class="other" id="again" href="/sign-in/code?next={{.Next}}" hidden>{{.W.Again}}</a>
  <script src="https://{{.FrontendAPI}}/npm/@clerk/clerk-js@{{.ScriptVersion}}/dist/clerk.browser.js" data-clerk-publishable-key="{{.PublishableKey}}" crossorigin="anonymous" async></script>
  <script nonce="{{.Nonce}}">
    (async function () {
      function failed() {
        document.getElementById("finishing").hidden = true;
        document.getElementById("not-finished").hidden = false;
        document.getElementById("again").hidden = false;
      }
      try {
        var waited = 0;
        while (typeof window.Clerk === "undefined" && waited < 15000) {
          await new Promise(function (r) { setTimeout(r, 50); });
          waited += 50;
        }
        if (typeof window.Clerk === "undefined") return failed();
        await window.Clerk.load({ publishableKey: {{.PublishableKey}} });
        // Another sign-in left in this browser is never used.
        if (window.Clerk.session) await window.Clerk.signOut();
        var attempt = await window.Clerk.client.signIn.create({ strategy: "ticket", ticket: {{.Ticket}} });
        if (attempt.status !== "complete") return failed();
        await window.Clerk.setActive({ session: attempt.createdSessionId });
        window.location.replace({{.Done}});
      } catch (e) {
        failed();
      }
    })();
  </script>
  {{else if eq .Step "signin"}}
  <div id="sign-in"></div>
  <a class="other" id="email-code-link" href="{{.Link}}">{{.W.Link}}</a>
  <p class="problem" id="not-loaded" role="alert" hidden>{{.W.NotLoaded}}</p>
  <script src="https://{{.FrontendAPI}}/npm/@clerk/clerk-js@{{.ScriptVersion}}/dist/clerk.browser.js" data-clerk-publishable-key="{{.PublishableKey}}" crossorigin="anonymous" async></script>
  <script src="https://{{.FrontendAPI}}/npm/@clerk/ui@{{.UIVersion}}/dist/ui.browser.js" crossorigin="anonymous" async></script>
  <script nonce="{{.Nonce}}">
    (async function () {
      var box = document.getElementById("sign-in");
      function notLoaded() { box.hidden = true; document.getElementById("not-loaded").hidden = false; }
      try {
        var waited = 0;
        while ((typeof window.Clerk === "undefined" || typeof window.__internal_ClerkUICtor === "undefined") && waited < 15000) {
          await new Promise(function (r) { setTimeout(r, 50); });
          waited += 50;
        }
        if (typeof window.Clerk === "undefined" || typeof window.__internal_ClerkUICtor === "undefined") return notLoaded();
        await window.Clerk.load({ publishableKey: {{.PublishableKey}}, ui: { ClerkUI: window.__internal_ClerkUICtor } });
        var done = {{.Done}};
        window.Clerk.mountSignIn(box, { withSignUp: true, forceRedirectUrl: done, signUpForceRedirectUrl: done });
      } catch (e) {
        notLoaded();
      }
    })();
  </script>
  {{end}}
</main>
</body>
</html>
`))

// SignInSettingPath is the person's setting, Settings › Sign-in: the web's,
// and the page the apps open in their sheet to turn codes on (turning them
// on needs a recent sign-in, which an app's own session never is).
const SignInSettingPath = "/dashboard/sign-in"

// SignInSettingHandler serves the setting, behind RequireSession.
type SignInSettingHandler struct {
	api     *apiclient.Client
	bearers BearerSource
	logger  *slog.Logger
}

// NewSignInSettingHandler wires the page.
func NewSignInSettingHandler(api *apiclient.Client, bearers BearerSource, logger *slog.Logger) *SignInSettingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SignInSettingHandler{api: api, bearers: bearers, logger: logger}
}

// Register mounts the page and its toggle.
func (h *SignInSettingHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+SignInSettingPath, h.show)
	mux.HandleFunc("POST "+SignInSettingPath, h.set)
}

type settingView struct {
	Lang, Nonce    string
	W              words.CodeSignIn
	On, Available  bool
	Saved, Problem string
}

func (h *SignInSettingHandler) bearer(r *http.Request) (string, bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		return "", false
	}
	if h.bearers != nil {
		b, err := h.bearers.BearerFor(r.Context(), s)
		return b, err == nil
	}
	return s.Bearer, s.Bearer != ""
}

func (h *SignInSettingHandler) show(w http.ResponseWriter, r *http.Request) {
	lang := appSignInLang(r)
	v := settingView{Lang: lang, W: words.CodeSignInIn(lang)}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(SignInSettingPath), http.StatusFound)
		return
	}
	set, err := h.api.SignInSetting(r.Context(), bearer)
	if err != nil {
		v.Problem = v.W.Unavailable
	}
	v.On, v.Available = set.EmailCode, set.Available
	if r.URL.Query().Get("saved") == "1" {
		v.Saved = v.W.SettingSaved
	}
	h.render(w, r, v)
}

// set turns codes on or off. On needs a recent sign-in, never a code one:
// the person signs in again and comes back here.
func (h *SignInSettingHandler) set(w http.ResponseWriter, r *http.Request) {
	if !fromThisPage(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(SignInSettingPath), http.StatusSeeOther)
		return
	}
	on := r.PostFormValue("email_code") == "on"
	_, err := h.api.SetSignInCode(r.Context(), bearer, on)
	switch {
	case err == nil:
		http.Redirect(w, r, SignInSettingPath+"?saved=1", http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrReauthRequired):
		http.Redirect(w, r, "/sign-out?next="+url.QueryEscape(SignInSettingPath), http.StatusSeeOther)
	default:
		h.logger.WarnContext(r.Context(), "sign-in setting: set failed", slog.String("err", err.Error()))
		lang := appSignInLang(r)
		v := settingView{Lang: lang, W: words.CodeSignInIn(lang), On: !on, Available: !errors.Is(err, apiclient.ErrCodeOff)}
		v.Problem = v.W.Unavailable
		if errors.Is(err, apiclient.ErrCodeSecondFactor) {
			v.Problem = v.W.SecondFactor
		}
		h.render(w, r, v)
	}
}

func (h *SignInSettingHandler) render(w http.ResponseWriter, r *http.Request, v settingView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if err := settingPage.Execute(w, v); err != nil {
		h.logger.ErrorContext(r.Context(), "sign-in setting: render", slog.String("err", err.Error()))
	}
}

var settingPage = template.Must(template.New("setting").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>{{.W.SettingTitle}}</title>
  <style>
    :root { color-scheme: light dark; --ink: #1b1b1f; --soft: #5b5b66; --bg: #ffffff; --line: #e4e4ea; --amber: #c77d00; --bad: #b42318; }
    @media (prefers-color-scheme: dark) { :root { --ink: #ececf1; --soft: #a0a0ab; --bg: #111114; --line: #2a2a31; --amber: #f0a43a; --bad: #f97066; } }
    body { margin: 0; font: 16px/1.45 system-ui, -apple-system, sans-serif; color: var(--ink); background: var(--bg); }
    main { max-width: 520px; margin: 0 auto; padding: 24px 16px 40px; }
    h1 { font-size: 24px; margin: 0 0 16px; }
    .row { border: 1px solid var(--line); border-radius: 12px; padding: 16px; }
    .row h2 { font-size: 17px; margin: 0 0 6px; display: flex; justify-content: space-between; gap: 12px; }
    .row p { color: var(--soft); margin: 0 0 14px; }
    .state { font-weight: 600; }
    .go { padding: 12px 16px; border-radius: 10px; font: inherit; font-weight: 600; background: var(--amber); color: #fff; border: 0; }
    .note { margin: 0 0 16px; }
    .bad { color: var(--bad); }
  </style>
</head>
<body>
<main>
  <h1>{{.W.SettingTitle}}</h1>
  {{if .Saved}}<p class="note" role="status">{{.Saved}}</p>{{end}}
  {{if .Problem}}<p class="note bad" role="alert">{{.Problem}}</p>{{end}}
  <div class="row">
    <h2>{{.W.SettingLabel}} <span class="state">{{if .On}}{{.W.SettingOn}}{{else}}{{.W.SettingOff}}{{end}}</span></h2>
    <p>{{.W.SettingHelp}}</p>
    {{if .On}}
    <form method="post"><input type="hidden" name="email_code" value="off"><button class="go" type="submit">{{.W.TurnOff}}</button></form>
    {{else if .Available}}
    <form method="post"><input type="hidden" name="email_code" value="on"><button class="go" type="submit">{{.W.TurnOn}}</button></form>
    {{else}}
    <p>{{.W.SettingNotAvailable}}</p>
    {{end}}
  </div>
</main>
</body>
</html>
`))
