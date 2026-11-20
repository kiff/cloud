package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

func newTestSessionManager(t *testing.T) *session.Manager {
	t.Helper()
	m, err := session.NewManager([]byte("a-test-secret-of-at-least-thirty-two-bytes"))
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	return m
}

func TestSignInForm_RendersForUnauthenticatedUser(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/sign-in", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="subject"`) {
		t.Fatalf("form missing")
	}
}

func TestSignInForm_RedirectsWhenSignedIn(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	// Mint a valid cookie and replay /sign-in.
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject: "alice", Bearer: "alice", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := rec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/sign-in", nil)
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec2.Code)
	}
	if loc := rec2.Header().Get("Location"); loc != "/dashboard" {
		t.Fatalf("Location = %q, want /dashboard", loc)
	}
}

func TestSignInSubmit_HappyPath_IssuesSessionAndRedirects(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	form := url.Values{"subject": {"alice@example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != session.CookieName {
		t.Fatalf("expected one session cookie")
	}
	// Verify the cookie reads back.
	verify := httptest.NewRequest(http.MethodGet, "/", nil)
	verify.AddCookie(cookies[0])
	got, err := mgr.Read(verify)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Subject != "alice@example.com" || got.Email != "alice@example.com" {
		t.Fatalf("session = %+v", got)
	}
}

func TestSignInSubmit_RejectedReturns401(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil, "only-alice@example.com")
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	form := url.Values{"subject": {"mallory@example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestSignInSubmit_RespectsNextParam(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	form := url.Values{"subject": {"alice"}}
	req := httptest.NewRequest(http.MethodPost, "/sign-in?next=/dashboard/receipts/abc", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if loc := rec.Header().Get("Location"); loc != "/dashboard/receipts/abc" {
		t.Fatalf("Location = %q, want next path honored", loc)
	}
}

func TestSignInSubmit_RejectsCrossOriginNext(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	form := url.Values{"subject": {"alice"}}
	req := httptest.NewRequest(http.MethodPost, "/sign-in?next=https://attacker.example/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Fatalf("Location = %q, want /dashboard (cross-origin next refused)", loc)
	}
}

func TestSignOut_ClearsCookieAndRedirects(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/sign-out", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
	if len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("expected an expired cookie clear")
	}
}

func TestRequireSession_RedirectsAnonymousToSignIn(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	protected := handlers.RequireSession(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/sign-in?next=") {
		t.Fatalf("Location = %q, want /sign-in?next=...", loc)
	}
}

func TestRequireSession_AllowsValidSession(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	called := false
	var ctxSession session.Session
	protected := handlers.RequireSession(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		s, _ := handlers.SessionFrom(r.Context())
		ctxSession = s
	}))

	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject: "alice", Bearer: "alice", Email: "alice@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := rec.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	protected.ServeHTTP(rec2, req)

	if !called {
		t.Fatalf("protected handler not called")
	}
	if ctxSession.Subject != "alice" {
		t.Fatalf("ctxSession = %+v", ctxSession)
	}
}

func TestSessionFrom_ReturnsFalseWithoutMiddleware(t *testing.T) {
	t.Parallel()
	if _, ok := handlers.SessionFrom(context.Background()); ok {
		t.Fatalf("SessionFrom should return false on bare context")
	}
}

// fakeClerkProvider implements both auth.Provider and the
// FrontendAPI/PublishableKey introspection the callback-page
// handler uses to detect Clerk-mode providers, without dragging
// the real Clerk SDK and JWKS roundtrip into the handler tests.
type fakeClerkProvider struct {
	auth.Provider
	frontend string
	pk       string
}

func (f *fakeClerkProvider) FrontendAPI() string    { return f.frontend }
func (f *fakeClerkProvider) PublishableKey() string { return f.pk }

func TestSignInCallbackPage_RendersBridgeForClerkProvider(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := &fakeClerkProvider{
		Provider: auth.NewStubProvider(nil),
		frontend: "clerk.kiff.dev",
		pk:       "pk_live_xyz",
	}
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback?next=/dashboard/receipts/abc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"clerk.browser.js",
		"clerk.kiff.dev",
		"pk_live_xyz",
		"/dashboard/receipts/abc",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("callback HTML missing %q\n---\n%s", want, body)
		}
	}
	// Regression guard: html/template auto-quotes string values
	// in <script> context. Using `%q` on top of that produces
	// "\"pk_...\"" which crashes Clerk.load(). The bridge must
	// emit the publishable key as a single-quoted JSON string.
	if strings.Contains(body, `\"pk_live`) || strings.Contains(body, `"\"pk_live`) {
		t.Fatalf("publishable key got double-quoted; html/template already quotes script values:\n%s", body)
	}
}

func TestSignInCallbackPage_NonClerkProviderRedirects(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := auth.NewStubProvider(nil)
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/sign-in" {
		t.Fatalf("Location = %q, want /sign-in", loc)
	}
}

func TestSignInCallbackPage_RejectsCrossOriginNext(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	prov := &fakeClerkProvider{
		Provider: auth.NewStubProvider(nil),
		frontend: "clerk.kiff.dev",
		pk:       "pk_live_xyz",
	}
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback?next=https://attacker.example/x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "attacker.example") {
		t.Fatalf("cross-origin next leaked into body:\n%s", body)
	}
	if !strings.Contains(body, "/dashboard") {
		t.Fatalf("expected fallback to /dashboard:\n%s", body)
	}
}

// newTestClerkMux wires a Clerk-shaped provider + session manager
// and returns the mux. Shared by the loop-breaker tests.
func newTestClerkMux(t *testing.T) (*http.ServeMux, *session.Manager) {
	t.Helper()
	mgr := newTestSessionManager(t)
	prov := &fakeClerkProvider{
		Provider: auth.NewStubProvider(nil),
		frontend: "clerk.kiff.dev",
		pk:       "pk_live_xyz",
	}
	mux := http.NewServeMux()
	handlers.NewAuthHandler(prov, mgr, nil).Register(mux)
	return mux, mgr
}

// guardCookie returns the kiff_cb_attempts cookie from a response,
// or nil when absent.
func guardCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "kiff_cb_attempts" {
			return c
		}
	}
	return nil
}

// The observed prod loop: clerk.js keeps a session, our POST mints
// kiff_session every lap (success), but clerk.browser.js bounces back to
// the Account Portal → the callback is re-served → loop, and because each
// POST "succeeds" the landing counter resets and never trips. The fix:
// once a valid kiff_session exists, the callback GET redirects straight in
// and never re-serves the Clerk JS bridge, so the bounce can't recur.
func TestSignInCallbackPage_AlreadySignedInShortCircuits(t *testing.T) {
	t.Parallel()
	mux, mgr := newTestClerkMux(t)

	// Mint a valid session cookie, as a completed sign-in would.
	rec0 := httptest.NewRecorder()
	if err := mgr.Issue(rec0, session.Session{Subject: "user_x", Bearer: "tok"}); err != nil {
		t.Fatalf("issue session: %v", err)
	}
	var sc *http.Cookie
	for _, c := range rec0.Result().Cookies() {
		if c.Name == session.CookieName {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("no session cookie issued")
	}

	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback?next=/dashboard/build", nil)
	req.AddCookie(sc)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (signed-in short-circuit); body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/build" {
		t.Errorf("Location = %q, want /dashboard/build", loc)
	}
	if strings.Contains(rec.Body.String(), "clerk.browser.js") {
		t.Errorf("a signed-in callback must NOT re-serve the Clerk JS bridge — that is the loop")
	}
}

func TestSignInCallbackPage_IncrementsLoopGuard(t *testing.T) {
	t.Parallel()
	mux, _ := newTestClerkMux(t)

	// First landing: no guard cookie yet → bridge served, counter = 1.
	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "clerk.browser.js") {
		t.Fatalf("first landing should serve the JS bridge")
	}
	gc := guardCookie(rec.Result())
	if gc == nil || gc.Value != "1" {
		t.Fatalf("guard cookie = %+v, want value 1", gc)
	}

	// Second landing carrying counter = 1 → still bridge, counter = 2.
	req2 := httptest.NewRequest(http.MethodGet, "/sign-in/callback", nil)
	req2.AddCookie(&http.Cookie{Name: "kiff_cb_attempts", Value: "1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "clerk.browser.js") {
		t.Fatalf("second landing should still serve the JS bridge")
	}
	if gc := guardCookie(rec2.Result()); gc == nil || gc.Value != "2" {
		t.Fatalf("guard cookie = %+v, want value 2", gc)
	}
}

func TestSignInCallbackPage_LoopBreakerServesTerminalPage(t *testing.T) {
	t.Parallel()
	mux, _ := newTestClerkMux(t)

	// Counter already at the threshold → terminal page, no Clerk JS.
	req := httptest.NewRequest(http.MethodGet, "/sign-in/callback", nil)
	req.AddCookie(&http.Cookie{Name: "kiff_cb_attempts", Value: "3"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "clerk.browser.js") {
		t.Fatalf("terminal page must NOT load Clerk JS (would re-enter the loop):\n%s", body)
	}
	if !strings.Contains(body, "couldn’t complete sign-in") {
		t.Fatalf("terminal page missing its explanatory copy:\n%s", body)
	}
	// The counter is retired so the manual retry starts clean.
	if gc := guardCookie(rec.Result()); gc == nil || gc.MaxAge >= 0 {
		t.Fatalf("expected guard cookie to be cleared, got %+v", gc)
	}
}

func TestSignInForm_ResetsLoopGuard(t *testing.T) {
	t.Parallel()
	mux, _ := newTestClerkMux(t)

	req := httptest.NewRequest(http.MethodGet, "/sign-in", nil)
	req.AddCookie(&http.Cookie{Name: "kiff_cb_attempts", Value: "3"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	gc := guardCookie(rec.Result())
	if gc == nil || gc.MaxAge >= 0 {
		t.Fatalf("GET /sign-in should clear the loop guard, got %+v", gc)
	}
}

func TestSignInCallbackSubmit_ClearsLoopGuardOnSuccess(t *testing.T) {
	t.Parallel()
	mux, mgr := newTestClerkMux(t)

	// fakeClerkProvider delegates VerifySignIn to the stub, which
	// reads a form "subject" — so a form POST stands in for a
	// verified Clerk callback here.
	form := url.Values{"subject": {"alice@example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/sign-in/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "kiff_cb_attempts", Value: "2"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (JSON redirect envelope)", rec.Code)
	}
	resp := rec.Result()
	if gc := guardCookie(resp); gc == nil || gc.MaxAge >= 0 {
		t.Fatalf("successful sign-in should clear the loop guard, got %+v", gc)
	}
	// A real session cookie must also have been issued.
	verify := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range resp.Cookies() {
		if c.Name == session.CookieName {
			verify.AddCookie(c)
		}
	}
	if _, err := mgr.Read(verify); err != nil {
		t.Fatalf("expected a valid session cookie after callback submit: %v", err)
	}
}

// A sign-in that comes back to the tafs app's sign-in page is stamped with that request, so the page answers it at
// once; any other sign-in isn't, so the page asks.
func TestSignInSubmit_StampsTheAppSignInRequestItComesBackTo(t *testing.T) {
	t.Parallel()
	for next, want := range map[string]string{
		"/sign-in/app?request=rq_0123456789abcdefABCDEF": "rq_0123456789abcdefABCDEF",
		"/dashboard": "",
	} {
		mgr := newTestSessionManager(t)
		mux := http.NewServeMux()
		handlers.NewAuthHandler(auth.NewStubProvider(nil), mgr, nil).Register(mux)
		form := url.Values{"subject": {"ana@example.com"}}
		req := httptest.NewRequest(http.MethodPost, "/sign-in?next="+url.QueryEscape(next), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		verify := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			verify.AddCookie(c)
		}
		got, err := mgr.Read(verify)
		if err != nil || got.SignedInFor != want {
			t.Errorf("next %q: SignedInFor %q, want %q (%v)", next, got.SignedInFor, want, err)
		}
	}
}
