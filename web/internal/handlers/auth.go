package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/session"
)

// AuthHandler bundles the sign-in / sign-out routes the dashboard
// shell depends on. It owns the bridge between the web's auth
// Provider (which knows how to verify a sign-in form) and the
// session.Manager (which knows how to issue a cookie).
type AuthHandler struct {
	provider auth.Provider
	sessions *session.Manager
	logger   *slog.Logger
}

// NewAuthHandler wires the provider + session manager. logger
// may be nil; the package falls back to slog.Default().
func NewAuthHandler(provider auth.Provider, sessions *session.Manager, logger *slog.Logger) *AuthHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuthHandler{provider: provider, sessions: sessions, logger: logger}
}

// Register attaches the handler's routes to mux.
func (h *AuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /sign-in", h.signInForm)
	mux.HandleFunc("POST /sign-in", h.signInSubmit)
	// Clerk's hosted-page flow redirects back to /sign-in/callback
	// after sign-in. The GET renders a tiny templ shell that loads
	// Clerk's frontend SDK, asks it for a session JWT, and POSTs
	// it back to this same path. The POST verifies the JWT and
	// mints our session cookie.
	mux.HandleFunc("GET /sign-in/callback", h.signInCallbackPage)
	mux.HandleFunc("POST /sign-in/callback", h.signInCallbackSubmit)
	mux.HandleFunc("POST /sign-out", h.signOut)
	// Convenience: a GET /sign-out that the topbar can link to,
	// despite RFC-style state-changing-on-GET squeamishness. The
	// effect is bounded (cookie clear, redirect) and matches what
	// every dashboard ships.
	mux.HandleFunc("GET /sign-out", h.signOut)
}

// signInCallbackPage renders the JS-bridge shell. The page loads
// Clerk's frontend SDK from the configured frontend API origin
// and posts the resulting session JWT back to this same path
// (POST). Stub-mode providers don't use this route.
func (h *AuthHandler) signInCallbackPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	type clerkConfigProvider interface {
		FrontendAPI() string
		PublishableKey() string
	}
	cfg, ok := h.provider.(clerkConfigProvider)
	if !ok {
		// Non-Clerk providers shouldn't see this route. Defence in
		// depth: redirect to /sign-in.
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Loop-breaker. Stale Clerk client cookies can drive an infinite
	// redirect ping-pong between the Account Portal and this callback
	// (the bounce is initiated by clerk.browser.js, not our server).
	// Count callback landings server-side; once we cross the
	// threshold, serve a static terminal page that loads no Clerk JS,
	// so the loop cannot continue for any user. The counter resets on
	// a fresh sign-in (GET /sign-in) and on success (POST callback).
	secure := h.sessions.CookieSecure()
	attempts := readCallbackAttempts(r)
	if attempts >= maxCallbackAttempts {
		h.logger.WarnContext(r.Context(), "sign-in callback: loop-breaker tripped; serving terminal page",
			slog.String("provider", h.provider.Name()),
			slog.Int("attempts", attempts),
		)
		clearCallbackAttempts(w, secure)
		_ = renderLoopBreakPage(w, callbackLanguage(r))
		return
	}
	setCallbackAttempts(w, attempts+1, secure)

	next := r.URL.Query().Get("next")
	if !isSafeNext(next) {
		next = "/dashboard"
	}

	// Already signed in? Skip the Clerk JS bridge entirely and go
	// straight to the destination. This is the fix the landing-counter
	// alone couldn't deliver (/#541): the observed prod loop is NOT
	// "clerk.js finds no session" — it's the opposite. clerk.js finds a
	// session, our POST verifies it and mints kiff_session (success), but
	// clerk.browser.js re-running its handshake on this page bounces back
	// to the Account Portal *after* the mint — so every lap the POST
	// "succeeds", the loop-breaker counter is cleared, and the ping-pong
	// never trips the terminal page (the journal showed a session issued
	// every ~2s for minutes). Reading our own session cookie here breaks
	// the loop server-side, before any Clerk JS loads: once a session is
	// minted, the callback stops re-running the bridge. Placed after the
	// counter increment so a pathological loop still trips the backstop.
	if _, err := h.sessions.Read(r); err == nil {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}

	_ = renderCallbackPage(w, callbackPageData{
		FrontendAPI:    cfg.FrontendAPI(),
		PublishableKey: cfg.PublishableKey(),
		PostPath:       "/sign-in/callback",
		Next:           next,
		Lang:           callbackLanguage(r),
	})
}

// signInCallbackSubmit handles the JSON POST from the JS bridge.
// Body: {"token": "<clerk-session-jwt>", "next": "/dashboard"}.
// On success: mints our session cookie and returns
// {"redirect": "/dashboard"} so the page can window.location to it.
func (h *AuthHandler) signInCallbackSubmit(w http.ResponseWriter, r *http.Request) {
	id, err := h.provider.VerifySignIn(r.Context(), r)
	if err != nil {
		h.logger.WarnContext(r.Context(), "sign-in callback: verify failed",
			slog.String("provider", h.provider.Name()),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	if err := h.sessions.Issue(w, session.Session{
		Subject:     id.Subject,
		Email:       id.Email,
		Bearer:      id.Bearer,
		SessionID:   id.SessionID,
		DisplayName: id.DisplayName,
	}); err != nil {
		h.logger.ErrorContext(r.Context(), "sign-in callback: issue session failed",
			slog.String("subject", id.Subject),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "issue session"})
		return
	}
	h.logger.InfoContext(r.Context(), "sign-in callback: issued session",
		slog.String("provider", h.provider.Name()),
		slog.String("subject", id.Subject),
	)
	// Sign-in completed: retire the loop-breaker counter so it can
	// never shadow a future sign-in on this browser.
	clearCallbackAttempts(w, h.sessions.CookieSecure())

	// The provider already consumed the body to verify the JWT,
	// so we use the URL's `next` (set on the GET that rendered the
	// callback page and preserved by the inline JS as a query
	// parameter on the POST). Defaults to /dashboard.
	next := r.URL.Query().Get("next")
	if !isSafeNext(next) {
		next = "/dashboard"
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": next})
}

// signInForm renders the provider's sign-in surface. For the
// stub provider this is an HTML form. For Clerk (follow-up
// slice) this will redirect to Clerk's hosted sign-in URL.
//
// If the user is already signed in (a valid session cookie is
// present), they're redirected to /dashboard instead. This is
// the desired UX for the bookmark-the-sign-in-page case.
func (h *AuthHandler) signInForm(w http.ResponseWriter, r *http.Request) {
	// A fresh sign-in attempt resets the callback loop-breaker, so a
	// user who hit the terminal page gets a genuinely clean retry
	// rather than tripping the counter again on the first bounce.
	clearCallbackAttempts(w, h.sessions.CookieSecure())
	if _, err := h.sessions.Read(r); err == nil {
		http.Redirect(w, r, redirectTarget(r), http.StatusFound)
		return
	}
	h.provider.SignInForm(w, r)
}

// signInSubmit processes the form. On success it issues a
// session cookie and redirects to /dashboard (or to the
// "next=" query param when valid). On failure it logs and
// re-renders the form with an error message.
func (h *AuthHandler) signInSubmit(w http.ResponseWriter, r *http.Request) {
	id, err := h.provider.VerifySignIn(r.Context(), r)
	if err != nil {
		h.logger.InfoContext(r.Context(), "sign-in: rejected",
			slog.String("provider", h.provider.Name()),
			slog.String("err", err.Error()),
		)
		// Render the form with the error rather than redirecting,
		// so the user sees what went wrong without losing their
		// place. The stub provider's default renderer ignores the
		// error message; the templ-backed renderer in cmd/web
		// surfaces it.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>Sign-in failed</h1><p>` +
			htmlEscape(err.Error()) + `</p><p><a href="/sign-in">try again</a></p></body></html>`))
		return
	}

	if err := h.sessions.Issue(w, session.Session{
		Subject:     id.Subject,
		Email:       id.Email,
		Bearer:      id.Bearer,
		SessionID:   id.SessionID,
		DisplayName: id.DisplayName,
	}); err != nil {
		h.logger.ErrorContext(r.Context(), "sign-in: issue session failed",
			slog.String("subject", id.Subject),
			slog.String("err", err.Error()),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.logger.InfoContext(r.Context(), "sign-in: issued session",
		slog.String("provider", h.provider.Name()),
		slog.String("subject", id.Subject),
	)
	http.Redirect(w, r, redirectTarget(r), http.StatusSeeOther)
}

// signOut clears the cookie and redirects to /. Idempotent —
// calling it without a session is a no-op.
//
// When the configured provider can revoke a session server-side
// (Clerk does via its Backend API), we call that first so Clerk's
// own session record is also invalidated. The revocation is
// best-effort: a failure is logged but does not block sign-out —
// the local session cookie is always cleared regardless.
//
// Note: Clerk's Account Portal does NOT expose a /sign-out page
// (accounts.kiff.dev/sign-out returns 404). We must revoke
// server-side and redirect straight to / — not through Clerk's
// hosted UI.
func (h *AuthHandler) signOut(w http.ResponseWriter, r *http.Request) {
	// Read the session before clearing so we have the SessionID
	// needed to revoke server-side.
	sess, sessErr := h.sessions.Read(r)
	h.sessions.Clear(w)

	type sessionRevoker interface {
		RevokeSession(ctx context.Context, sessionID string) error
	}
	if sr, ok := h.provider.(sessionRevoker); ok && sessErr == nil {
		if err := sr.RevokeSession(r.Context(), sess.SessionID); err != nil {
			h.logger.WarnContext(r.Context(), "sign-out: clerk session revocation failed (continuing)",
				slog.String("session_id", sess.SessionID),
				slog.String("err", err.Error()),
			)
		}
	}

	// ?next= sends the person straight back through sign-in, for a
	// change that needs a recent sign-in (the governance-roles
	// design step-up).
	if next := r.URL.Query().Get("next"); isSafeNext(next) {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// redirectTarget returns the path the sign-in flow should send
// the user to on success. Defaults to /dashboard. Honors a
// "next=" query parameter when it's a same-origin path (no
// scheme, no host) — this lets a middleware redirect that
// captured the user's original URL bring them back after
// signing in.
func redirectTarget(r *http.Request) string {
	const fallback = "/dashboard"
	next := strings.TrimSpace(r.URL.Query().Get("next"))
	if !isSafeNext(next) {
		// Same-origin only: no scheme or host, no "//" or backslash
		// that a browser would read as another site.
		return fallback
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return fallback
	}
	// The path stays escaped: decoding it would turn "%5C" back into
	// the backslash isSafeNext refused. The query is kept, so a
	// prefilled link such as /cards/new?task=refunds arrives filled in.
	target := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	return target
}

// htmlEscape is a minimal escaper for the inline error HTML.
// Used only by the stub-mode error path; real templ pages use
// templ's auto-escaping.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// RequireSession is the middleware that protects /dashboard/*.
// Unauthenticated visits get a 302 to /sign-in?next=<path>;
// authenticated requests have the resolved session attached to
// the context.
// signInNext is where sign-in returns to. A GET keeps its query, so a
// link such as /cards/new?task=refunds arrives with its form filled in
// after signing in; anything else returns to the path alone.
func signInNext(r *http.Request) string {
	if r.Method == http.MethodGet && r.URL.RawQuery != "" {
		return r.URL.Path + "?" + r.URL.RawQuery
	}
	return r.URL.Path
}

func RequireSession(sessions *session.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, err := sessions.Read(r)
			if err != nil {
				if errors.Is(err, session.ErrNoSession) || errors.Is(err, session.ErrInvalid) {
					target := "/sign-in?next=" + url.QueryEscape(signInNext(r))
					http.Redirect(w, r, target, http.StatusFound)
					return
				}
				// Any other error (expired, malformed): clear the
				// stale cookie and bounce.
				sessions.Clear(w)
				target := "/sign-in?next=" + url.QueryEscape(signInNext(r))
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			ctx := withSession(r.Context(), s)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sessionContextKey is the context key the middleware uses to
// stash the verified session. Handlers downstream of
// RequireSession read it via SessionFrom.
type sessionContextKey struct{}

func withSession(ctx context.Context, s session.Session) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, s)
}

// SessionFrom returns the verified session attached by
// RequireSession. The bool is false when no session is on the
// context — handlers that strictly require authentication should
// wrap themselves in RequireSession instead of branching on this.
func SessionFrom(ctx context.Context) (session.Session, bool) {
	s, ok := ctx.Value(sessionContextKey{}).(session.Session)
	return s, ok
}
