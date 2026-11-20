package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/session"
)

const appReq = "rq_0123456789abcdefABCDEF"

// appAPI is apps/api's appsignin as the page sees it: approve and deny answer
// with status and body, and count their calls and requests.
type appAPI struct {
	approves, denies atomic.Int32
	status           int
	body             string
	bearer, request  atomic.Value
}

func newAppAPI(t *testing.T, status int, body string) (*appAPI, *apiclient.Client) {
	t.Helper()
	a := &appAPI{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Request string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.request.Store(in.Request)
		a.bearer.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		switch r.URL.Path {
		case "/v1/auth/app/approve":
			a.approves.Add(1)
		case "/v1/auth/app/deny":
			a.denies.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

// script is a sign-in provider with a browser script.
type script struct{ auth.Provider }

func (script) FrontendAPI() string    { return "clerk.example.test" }
func (script) PublishableKey() string { return "pk_test_x" }

// appSignIn is the page on a mux, with sessions, the API, and the provider.
func appSignIn(t *testing.T, api *apiclient.Client, provider auth.Provider, now time.Time) (*http.ServeMux, *session.Manager) {
	t.Helper()
	sessions := testSessions(t)
	h := NewAppSignInHandler(true, api, sessions, nil, provider, nil)
	h.now = func() time.Time { return now }
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, sessions
}

// signedInAs adds Ana's KIFF session cookie to r.
func signedInAs(t *testing.T, sessions *session.Manager, r *http.Request) *http.Request {
	t.Helper()
	return signedInWith(t, sessions, r, "ana@example.com")
}

// signedInWith adds a KIFF session for email to r.
func signedInWith(t *testing.T, sessions *session.Manager, r *http.Request, email string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, session.Session{Subject: "u_ana", Email: email, Bearer: "tok_ana", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

// formMark is the mark the form leaves for request, sealed by sessions.
func formMark(sessions *session.Manager, request string, at time.Time) *http.Cookie {
	return &http.Cookie{Name: appSignInCookie, Value: sessions.Seal(appSignInSeal, request+"."+strconv.FormatInt(at.Unix(), 10))}
}

func serve(mux *http.ServeMux, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func markOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == appSignInCookie {
			return c
		}
	}
	return nil
}

var noon = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestAppSignIn_NoSessionShowsTheFormInTheAppsLanguageAndMarksIt(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c&state=s"}`)
	mux, sessions := appSignIn(t, client, script{}, noon)
	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq+"&ui_locales=de&login_hint=ana%40example.com", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<html lang="de">`, "mountSignIn", "withSignUp: true", `"ana@example.com"`, "clerk.example.test", "if (window.Clerk.session) await window.Clerk.signOut()"} {
		if !strings.Contains(body, want) {
			t.Errorf("the form misses %q", want)
		}
	}
	// Once signed in, the callback mints KIFF's session and comes back here, in the app's language.
	back := "/sign-in/callback?next=" + url.QueryEscape("/sign-in/app?request="+appReq+"&ui_locales=de")
	if !strings.Contains(body, strings.ReplaceAll(back, "&", `&`)) {
		t.Errorf("the form doesn't come back to this request through the callback:\n%s", body)
	}
	mark := markOf(rec)
	if mark == nil {
		t.Fatal("no mark")
	}
	opened, sealed := sessions.Open(appSignInSeal, mark.Value)
	if !sealed || opened != appReq+"."+strconv.FormatInt(noon.Unix(), 10) || strings.Contains(mark.Value, appReq) || !mark.HttpOnly || mark.Path != "/sign-in/app" || mark.SameSite != http.SameSiteLaxMode {
		t.Fatalf("the form's mark = %+v", mark)
	}
	if api.approves.Load()+api.denies.Load() != 0 {
		t.Errorf("showing the form answered the request")
	}
}

func TestAppSignIn_TheHintIsTextNeverMarkup(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusOK, "{}")
	mux, _ := appSignIn(t, client, script{}, noon)
	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq+"&login_hint="+url.QueryEscape(`"</script><script>alert(1)</script>`), nil))
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Fatalf("the hint was written as markup:\n%s", rec.Body.String())
	}
}

func TestAppSignIn_WithNoProviderScriptTheFormIsSignIns(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusOK, "{}")
	mux, _ := appSignIn(t, client, nil, noon)
	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sign-in?next="+url.QueryEscape("/sign-in/app?request="+appReq) {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if markOf(rec) == nil {
		t.Fatal("no mark for the form")
	}
}

func TestAppSignIn_BackFromTheFormAnswersOnceAndSendsTheSheetToTheApp(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c1&state=s1"}`)
	mux, sessions := appSignIn(t, client, script{}, noon)
	r := signedInAs(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
	r.AddCookie(formMark(sessions, appReq, noon.Add(-2*time.Minute)))
	rec := serve(mux, r)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "dev.kiff.tafs:/oauth?code=c1&state=s1" {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if api.approves.Load() != 1 || api.request.Load() != appReq || api.bearer.Load() != "tok_ana" {
		t.Errorf("approve: %d calls, request %v, bearer %v", api.approves.Load(), api.request.Load(), api.bearer.Load())
	}
	// Used once: the mark is cleared.
	if m := markOf(rec); m == nil || m.MaxAge >= 0 {
		t.Errorf("the mark wasn't cleared: %+v", m)
	}
}

func TestAppSignIn_ASessionWithoutThisRequestsMarkIsAskedNeverApproved(t *testing.T) {
	t.Parallel()
	elsewhere, _ := session.NewManager([]byte(strings.Repeat("z", 32)))
	for name, mark := range map[string]func(*session.Manager) *http.Cookie{
		"no mark": func(*session.Manager) *http.Cookie { return nil },
		"another request": func(m *session.Manager) *http.Cookie {
			return formMark(m, "rq_other0123456789abcdef", noon.Add(-time.Minute))
		},
		"shown too long ago":  func(m *session.Manager) *http.Cookie { return formMark(m, appReq, noon.Add(-appSignInFormTTL)) },
		"shown in the future": func(m *session.Manager) *http.Cookie { return formMark(m, appReq, noon.Add(time.Minute)) },
		"the bare value": func(*session.Manager) *http.Cookie {
			return &http.Cookie{Name: appSignInCookie, Value: appReq + "." + strconv.FormatInt(noon.Unix(), 10)}
		},
		"sealed elsewhere": func(*session.Manager) *http.Cookie { return formMark(elsewhere, appReq, noon.Add(-time.Minute)) },
		"sealed for another purpose": func(m *session.Manager) *http.Cookie {
			return &http.Cookie{Name: appSignInCookie, Value: m.Seal("flash", appReq+"."+strconv.FormatInt(noon.Unix(), 10))}
		},
		"a mark with no time": func(m *session.Manager) *http.Cookie {
			return &http.Cookie{Name: appSignInCookie, Value: m.Seal(appSignInSeal, appReq+".x")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c&state=s"}`)
			mux, sessions := appSignIn(t, client, script{}, noon)
			r := signedInAs(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
			if c := mark(sessions); c != nil {
				r.AddCookie(c)
			}
			rec := serve(mux, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range []string{"Continue as ana@example.com", "Use another account", "Cancel", `value="` + appReq + `"`,
				`href="/sign-out?next=` + url.QueryEscape("/sign-in/app?request="+appReq) + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("the page misses %q", want)
				}
			}
			if strings.Contains(body, "mountSignIn") {
				t.Errorf("a signed-in person was shown the form")
			}
			if api.approves.Load() != 0 {
				t.Errorf("approved without the person's tap")
			}
		})
	}
}

func appPost(t *testing.T, mux *http.ServeMux, sessions *session.Manager, form string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/sign-in/app", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sessions != nil {
		r = signedInAs(t, sessions, r)
	}
	return serve(mux, r)
}

func TestAppSignIn_ContinueApprovesAndCancelDenies(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c2&state=s2"}`)
	mux, sessions := appSignIn(t, client, script{}, noon)
	rec := appPost(t, mux, sessions, "request="+appReq+"&answer=continue")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "dev.kiff.tafs:/oauth?code=c2&state=s2" || api.approves.Load() != 1 {
		t.Fatalf("continue: %d → %q, %d approves", rec.Code, rec.Header().Get("Location"), api.approves.Load())
	}

	api, client = newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?error=access_denied&state=s3"}`)
	mux, sessions = appSignIn(t, client, script{}, noon)
	rec = appPost(t, mux, sessions, "request="+appReq+"&answer=cancel")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "dev.kiff.tafs:/oauth?error=access_denied&state=s3" || api.denies.Load() != 1 || api.approves.Load() != 0 {
		t.Fatalf("cancel: %d → %q, %d denies, %d approves", rec.Code, rec.Header().Get("Location"), api.denies.Load(), api.approves.Load())
	}
}

func TestAppSignIn_NeverSendsTheSheetAnywhereButTheApp(t *testing.T) {
	t.Parallel()
	for _, to := range []string{"https://evil.example/oauth?code=c", "dev.kiff.tafs:/other?code=c", "dev.kiff.tafs:/oauth", "javascript:alert(1)"} {
		_, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"`+to+`"}`)
		mux, sessions := appSignIn(t, client, script{}, noon)
		rec := appPost(t, mux, sessions, "request="+appReq+"&answer=continue")
		if rec.Code != http.StatusBadGateway || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d → %q", to, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestAppSignIn_AnOlderSignInSignsInAgainAndComesBack(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusForbidden, `{"error":"reauth_required"}`)
	mux, sessions := appSignIn(t, client, script{}, noon)
	rec := appPost(t, mux, sessions, "request="+appReq+"&answer=continue&ui_locales=fr")
	want := "/sign-out?next=" + url.QueryEscape("/sign-in/app?request="+appReq+"&ui_locales=fr")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("got %d → %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

func TestAppSignIn_SaysWhenItCantGoOn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		body   string
		code   int
		says   string
	}{
		{http.StatusGone, `{"error":"this sign-in has expired; start again from the app"}`, http.StatusGone, "This sign-in has expired. Start again from the app."},
		{http.StatusInternalServerError, `{}`, http.StatusBadGateway, "We couldn’t sign you in just now. Start again from the app."},
		{http.StatusNotFound, `404 page not found`, http.StatusNotFound, ""},
	}
	for _, c := range cases {
		_, client := newAppAPI(t, c.status, c.body)
		mux, sessions := appSignIn(t, client, script{}, noon)
		rec := appPost(t, mux, sessions, "request="+appReq+"&answer=continue")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.says) || rec.Header().Get("Location") != "" {
			t.Errorf("API %d: got %d, %q", c.status, rec.Code, rec.Body.String())
		}
	}
}

func TestAppSignIn_RefusesAnythingButARequestID(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, "{}")
	mux, sessions := appSignIn(t, client, script{}, noon)
	for _, req := range []string{"", "short", strings.Repeat("a", 129), "rq_0123456789abcdef/../x", "rq_0123456789abcdef<b>"} {
		rec := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+url.QueryEscape(req), nil))
		if rec.Code != http.StatusBadRequest || markOf(rec) != nil {
			t.Errorf("GET %q: %d, mark %v", req, rec.Code, markOf(rec))
		}
		if rec := appPost(t, mux, sessions, "answer=continue&request="+url.QueryEscape(req)); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %q: %d", req, rec.Code)
		}
	}
	if api.approves.Load()+api.denies.Load() != 0 {
		t.Error("a malformed request reached the API")
	}
}

func TestAppSignIn_APostWithNoSessionGoesBackToTheForm(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, "{}")
	mux, _ := appSignIn(t, client, script{}, noon)
	rec := appPost(t, mux, nil, "request="+appReq+"&answer=continue")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/sign-in/app?request="+appReq || api.approves.Load() != 0 {
		t.Fatalf("got %d → %q, %d approves", rec.Code, rec.Header().Get("Location"), api.approves.Load())
	}
}

func TestAppSignIn_AnUnknownLanguageIsEnglish(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusOK, "{}")
	mux, _ := appSignIn(t, client, script{}, noon)
	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq+"&ui_locales=ja", nil))
	if !strings.Contains(rec.Body.String(), `<html lang="en">`) || !strings.Contains(rec.Body.String(), "Sign in to tafs") {
		t.Fatalf("not English:\n%s", rec.Body.String())
	}
}

func TestAppSignIn_OffSaysSoAndDoesNothing(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c&state=s"}`)
	sessions := testSessions(t)
	mux := http.NewServeMux()
	NewAppSignInHandler(false, client, sessions, nil, script{}, nil).Register(mux)
	marked := signedInAs(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
	marked.AddCookie(formMark(sessions, appReq, time.Now()))
	for _, r := range []*http.Request{marked, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil)} {
		rec := serve(mux, r)
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound || !strings.Contains(body, "Signing in inside the app isn’t on yet.") || markOf(rec) != nil || strings.Contains(body, "mountSignIn") {
			t.Errorf("GET off: %d, mark %v:\n%s", rec.Code, markOf(rec), body)
		}
	}
	if rec := appPost(t, mux, sessions, "request="+appReq+"&answer=continue"); rec.Code != http.StatusNotFound {
		t.Errorf("POST off: %d", rec.Code)
	}
	if api.approves.Load()+api.denies.Load() != 0 {
		t.Error("off, the page reached the API")
	}
}

func TestAppSignIn_IsNeverFramedAndRunsOnlyItsOwnAndThePinnedScript(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusOK, "{}")
	mux, sessions := appSignIn(t, client, script{}, noon)
	form := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
	asked := serve(mux, signedInAs(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil)))
	for name, rec := range map[string]*httptest.ResponseRecorder{"form": form, "continue as": asked} {
		csp := rec.Header().Get("Content-Security-Policy")
		if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s can be framed: %q", name, csp)
		}
		for _, want := range []string{"default-src 'none'", "form-action 'self'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP misses %q", name, want)
			}
		}
		scriptSrc, _, _ := strings.Cut(strings.SplitN(csp, "script-src", 2)[1], ";")
		if strings.Contains(scriptSrc, "unsafe-inline") || strings.Contains(scriptSrc, "unsafe-eval") {
			t.Errorf("%s: script-src lets any inline script run: %q", name, scriptSrc)
		}
	}
	body, csp := form.Body.String(), form.Header().Get("Content-Security-Policy")
	nonce, _, _ := strings.Cut(strings.SplitN(csp, "'nonce-", 2)[1], "'")
	if !strings.Contains(body, `<script nonce="`+nonce+`">`) {
		t.Errorf("the page's own script doesn't carry the CSP's nonce")
	}
	if !strings.Contains(body, "/npm/@clerk/clerk-js@"+providerScriptVersion+"/dist/clerk.browser.js") || strings.Contains(body, "@latest") {
		t.Errorf("the provider's script isn't the pinned version")
	}
	if !strings.Contains(csp, "script-src 'self' 'nonce-"+nonce+"' https://clerk.example.test ") {
		t.Errorf("script-src: %q", csp)
	}
	again := serve(mux, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil))
	if strings.Contains(again.Header().Get("Content-Security-Policy"), nonce) {
		t.Error("the nonce is reused")
	}
}

func TestAppSignIn_AnEmailWithMarkupIsText(t *testing.T) {
	t.Parallel()
	_, client := newAppAPI(t, http.StatusOK, "{}")
	mux, sessions := appSignIn(t, client, script{}, noon)
	email := `"><script>alert(1)</script><b>x</b>@example.com`
	rec := serve(mux, signedInWith(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil), email))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, "<b>x</b>") || !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt;@example.com") {
		t.Fatalf("the email was written as markup:\n%s", body)
	}
}

func TestAppSignIn_NoGETAnswersWithoutTheFormsMark(t *testing.T) {
	t.Parallel()
	api, client := newAppAPI(t, http.StatusOK, `{"redirect_to":"dev.kiff.tafs:/oauth?code=c&state=s"}`)
	mux, sessions := appSignIn(t, client, script{}, noon)
	for _, q := range []string{"&answer=continue", "&answer=cancel", "&signed_in=1"} {
		serve(mux, signedInAs(t, sessions, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq+q, nil)))
	}
	if api.approves.Load()+api.denies.Load() != 0 {
		t.Fatalf("a GET answered the request: %d approves, %d denies", api.approves.Load(), api.denies.Load())
	}
}
