package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/words"
)

// codeAPI is apps/api's codesignin as the page sees it.
type codeAPI struct {
	mu      sync.Mutex
	sends   []map[string]string
	verifys []map[string]string
	auth    []string
	// verify answers with this status and body.
	status int
	body   string
}

func newCodeAPI(t *testing.T) (*codeAPI, *apiclient.Client) {
	t.Helper()
	a := &codeAPI{status: http.StatusOK, body: `{"ticket":"sit_ticket_123"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.auth = append(a.auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/auth/code/send":
			a.sends = append(a.sends, in)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"sent_if_on"}`))
		case "/v1/auth/code/verify":
			a.verifys = append(a.verifys, in)
			w.WriteHeader(a.status)
			_, _ = w.Write([]byte(a.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

func codeRig(t *testing.T, on bool, provider auth.Provider) (*codeAPI, *http.ServeMux, *CodeSignInHandler) {
	t.Helper()
	api, client := newCodeAPI(t)
	h := NewCodeSignInHandler(on, client, testSessions(t), provider, func(*http.Request) string { return "FR" }, nil)
	h.now = func() time.Time { return noon }
	mux := http.NewServeMux()
	h.Register(mux)
	return api, mux, h
}

func codePost(path string, form url.Values, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone) AppleWebKit Version/18.0 Mobile Safari/604.1")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func flowCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == codeFlowCookie {
			return c
		}
	}
	return nil
}

// Asking for a code answers the same whatever the address, marks this
// browser with a flow, and calls KIFF with no credential.
func TestCodeSignIn_AskingAnswersTheSameAndMarksTheBrowser(t *testing.T) {
	api, mux, _ := codeRig(t, true, script{})
	var pages []string
	for _, a := range []string{"on@example.com", "nobody@example.com"} {
		rec := serve(mux, codePost(codeSignInPath, url.Values{"email": {a}, "next": {"/dashboard"}}))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", a, rec.Code)
		}
		c := flowCookie(rec)
		if c == nil || !c.HttpOnly || c.Path != codeSignInPath || c.MaxAge != int(codeFlowTTL.Seconds()) {
			t.Fatalf("%s: the flow cookie: %+v", a, c)
		}
		pages = append(pages, strings.ReplaceAll(rec.Body.String(), a, "ADDR"))
	}
	if pages[0] != pages[1] {
		t.Error("the page differs between an address with codes on and one with none")
	}
	if !strings.Contains(pages[0], strings.ReplaceAll(words.CodeSignInIn("en").Sent("x"), "x", "ADDR")) {
		t.Errorf("the same answer isn't said: %s", pages[0])
	}
	if len(api.sends) != 2 || api.sends[0]["flow"] == "" || api.sends[0]["flow"] == api.sends[1]["flow"] ||
		api.sends[0]["browser"] != "Safari" || api.sends[0]["country"] != "FR" || api.sends[0]["lang"] != "en" {
		t.Fatalf("sends: %+v", api.sends)
	}
	for _, a := range api.auth {
		if a != "" {
			t.Fatalf("a credential went with the call: %q", a)
		}
	}
}

// A code works only from the browser that asked: KIFF gets its flow, and
// the page finishes the sign-in with the provider's token, then the
// callback mints KIFF's session for where it was going.
func TestCodeSignIn_TheCodeIsCheckedForThisBrowserAndFinishes(t *testing.T) {
	api, mux, _ := codeRig(t, true, script{})
	next := "/sign-in/app?request=" + appReq
	asked := serve(mux, codePost(codeSignInPath, url.Values{"email": {"on@example.com"}, "next": {next}}))
	c := flowCookie(asked)
	rec := serve(mux, codePost(codeSignInPath+"/verify", url.Values{"code": {"123 456"}, "next": {next}}, c))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	if len(api.verifys) != 1 || api.verifys[0]["flow"] != api.sends[0]["flow"] || api.verifys[0]["email"] != "on@example.com" || api.verifys[0]["code"] != "123 456" {
		t.Fatalf("verify call: %+v", api.verifys)
	}
	body, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(body, `strategy: "ticket", ticket: "sit_ticket_123"`) {
		t.Errorf("the page doesn't finish with the token:\n%s", body)
	}
	if !strings.Contains(body, url.QueryEscape(next)) || !strings.Contains(body, "/sign-in/callback?next=") {
		t.Errorf("the page doesn't go to the callback for the sheet's request")
	}
	if !strings.Contains(csp, "https://clerk.example.test") || strings.Contains(csp, "unsafe-inline' 'nonce") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %q %q", csp, rec.Header().Get("Cache-Control"))
	}
	if gone := flowCookie(rec); gone == nil || gone.MaxAge != -1 {
		t.Errorf("the ask isn't spent: %+v", gone)
	}
	// Without the browser's mark, nothing is checked.
	rec = serve(mux, codePost(codeSignInPath+"/verify", url.Values{"code": {"123456"}}))
	if rec.Code != http.StatusGone || len(api.verifys) != 1 {
		t.Fatalf("no mark: %d, %d calls", rec.Code, len(api.verifys))
	}
}

// A wrong code and too many tries are said in the person's words; a post
// from another site is refused.
func TestCodeSignIn_WrongSlowAndCrossSite(t *testing.T) {
	api, mux, _ := codeRig(t, true, script{})
	c := flowCookie(serve(mux, codePost(codeSignInPath, url.Values{"email": {"on@example.com"}, "ui_locales": {"de"}})))
	api.status, api.body = http.StatusBadRequest, `{"error":"code_wrong"}`
	rec := serve(mux, codePost(codeSignInPath+"/verify", url.Values{"code": {"000000"}, "ui_locales": {"de"}}, c))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), words.CodeSignInIn("de").Wrong) {
		t.Fatalf("wrong: %d", rec.Code)
	}
	api.status, api.body = http.StatusTooManyRequests, `{"error":"slow_down"}`
	rec = serve(mux, codePost(codeSignInPath+"/verify", url.Values{"code": {"000000"}}, c))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), words.CodeSignInIn("en").SlowDown) {
		t.Fatalf("slow down: %d", rec.Code)
	}
	r := codePost(codeSignInPath, url.Values{"email": {"on@example.com"}})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := serve(mux, r); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
}

// Off, the pages say so and call nothing, and neither sign-in form has the
// link; on, both have it.
func TestCodeSignIn_OffShowsNothingOnShowsTheLink(t *testing.T) {
	api, mux, off := codeRig(t, false, script{})
	if rec := serve(mux, httptest.NewRequest(http.MethodGet, codeSignInPath, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("off: %d", rec.Code)
	}
	if rec := serve(mux, codePost(codeSignInPath, url.Values{"email": {"on@example.com"}})); rec.Code != http.StatusNotFound || len(api.sends) != 0 {
		t.Fatalf("off send: %d %d", rec.Code, len(api.sends))
	}
	if off.SignInPage(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sign-in", nil)) {
		t.Error("off, /sign-in is KIFF's page")
	}
	_, client := newAppAPI(t, http.StatusOK, "{}")
	for _, on := range []bool{false, true} {
		_, _, code := codeRig(t, on, script{})
		h := NewAppSignInHandler(true, client, testSessions(t), nil, script{}, nil).WithCodeSignIn(code)
		m := http.NewServeMux()
		h.Register(m)
		body := serve(m, httptest.NewRequest(http.MethodGet, "/sign-in/app?request="+appReq, nil)).Body.String()
		if has := strings.Contains(body, `id="email-code-link"`); has != on {
			t.Errorf("on=%v: the sheet's link shown=%v", on, has)
		}
		if on && !strings.Contains(body, "/sign-in/code?next=%2Fsign-in%2Fapp%3Frequest%3D"+appReq) {
			t.Errorf("the sheet's link doesn't come back to its request:\n%s", body)
		}
	}
	_, _, on := codeRig(t, true, script{})
	rec := httptest.NewRecorder()
	if !on.SignInPage(rec, httptest.NewRequest(http.MethodGet, "/sign-in?next=%2Fcards", nil)) {
		t.Fatal("on, /sign-in isn't KIFF's page")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="email-code-link"`) || !strings.Contains(body, "mountSignIn") || !strings.Contains(body, "/sign-in/callback?next=%2Fcards") {
		t.Errorf("/sign-in:\n%s", body)
	}
}

func TestCodeSignIn_TheMoreLanguagesReadTheirOwnWords(t *testing.T) {
	_, mux, _ := codeRig(t, true, script{})
	for _, c := range []struct {
		header, lang string
		rtl          bool
	}{
		{"ar-EG,ar;q=0.9,en;q=0.5", "ar", true},
		{"he", "he", true},
		{"dv", "dv", true},
		{"ko-KR,ko;q=0.9", "ko", false},
		{"th;q=0.9,xx", "th", false},
		{"de-CH,de;q=0.9", "de", false},
		{"xx", "en", false},
	} {
		req := httptest.NewRequest(http.MethodGet, codeSignInPath+"?next=%2Fcards", nil)
		req.Header.Set("Accept-Language", c.header)
		body := serve(mux, req).Body.String()
		w := words.CodeSignInIn(c.lang)
		if !strings.Contains(body, `<html lang="`+c.lang+`"`) || !strings.Contains(body, w.EmailLabel) || !strings.Contains(body, w.Send) {
			t.Errorf("%s: the page isn't in %s:\n%s", c.header, c.lang, body)
		}
		if has := strings.Contains(body, ` dir="rtl"`); has != c.rtl {
			t.Errorf("%s: dir=rtl is %v, want %v", c.header, has, c.rtl)
		}
		if c.lang != "en" && w.Send == words.CodeSignInIn("en").Send {
			t.Errorf("%s: the page's words are English", c.lang)
		}
	}
	// ui_locales (the app's language, or the last screen's) beats the browser's.
	req := httptest.NewRequest(http.MethodGet, codeSignInPath+"?next=%2Fcards&ui_locales=ja", nil)
	req.Header.Set("Accept-Language", "ar")
	if body := serve(mux, req).Body.String(); !strings.Contains(body, `<html lang="ja"`) {
		t.Errorf("ui_locales=ja with an Arabic browser:\n%s", body)
	}
}
