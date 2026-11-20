package appsignin_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/auth"
)

const page = "https://app.kiff.test/sign-in/app"

var verifier = strings.Repeat("v", 43) + "-._~0123456789"

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type fixture struct {
	t   *testing.T
	h   *appsignin.Handler
	mux *http.ServeMux
	now time.Time
}

func newFixture(t *testing.T, on bool) *fixture {
	f := &fixture{t: t, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	f.h = appsignin.New(on, page, appsignin.WithClock(func() time.Time { return f.now }))
	f.mux = http.NewServeMux()
	f.h.RegisterAuthenticated(f.mux)
	return f
}

func authorizeQuery(over map[string]string) url.Values {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {appsignin.ClientID},
		"redirect_uri":          {appsignin.RedirectURI},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {"state-0123456789abcdef"},
	}
	for k, v := range over {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	return q
}

// authorize returns the status and the Location it redirected to.
func (f *fixture) authorize(q url.Values) (int, *url.URL) {
	w := httptest.NewRecorder()
	f.h.Authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	loc, _ := url.Parse(w.Header().Get("Location"))
	return w.Code, loc
}

func (f *fixture) call(path string, id *auth.Identity, body string) (int, map[string]string) {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

var ana = auth.Identity{Subject: "user_ana", Tenant: "t_ana", Email: "ana@example.com", Method: auth.MethodSession}

// signIn runs authorize then approve, and returns the code the app gets back.
func (f *fixture) signIn() string {
	f.t.Helper()
	code, loc := f.authorize(authorizeQuery(nil))
	if code != http.StatusFound || loc.Scheme+"://"+loc.Host+loc.Path != page {
		f.t.Fatalf("authorize: %d %v", code, loc)
	}
	status, out := f.call("/v1/auth/app/approve", &ana, `{"request":"`+loc.Query().Get("request")+`"}`)
	if status != http.StatusOK {
		f.t.Fatalf("approve: %d %v", status, out)
	}
	back, _ := url.Parse(out["redirect_to"])
	if back.Scheme != "dev.kiff.tafs" || back.Path != "/oauth" || back.Query().Get("state") != "state-0123456789abcdef" {
		f.t.Fatalf("redirect: %v", back)
	}
	return back.Query().Get("code")
}

// The whole way: authorize sends the sheet to the sign-in page, the signed-in
// person's approve sends it back with a code, and the code with its verifier
// is the person, once.
func TestAnAppSignInRedeemsOnceForThePerson(t *testing.T) {
	f := newFixture(t, true)
	c := f.signIn()
	who, err := f.h.Redeem(context.Background(), appsignin.ClientID, c, verifier, appsignin.RedirectURI)
	if err != nil || who.Subject != "user_ana" || who.Tenant != "t_ana" || who.Email != "ana@example.com" {
		t.Fatalf("redeem: %+v %v", who, err)
	}
	if _, err := f.h.Redeem(context.Background(), appsignin.ClientID, c, verifier, appsignin.RedirectURI); !errors.Is(err, appsignin.ErrBadGrant) {
		t.Fatalf("a used code: %v", err)
	}
}

// A code is refused for a wrong verifier, redirect or client, after its 60
// seconds, or once a wrong try has spent it.
func TestACodeIsRefusedUnlessEverythingMatches(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		client, verifier, redirect string
		wait                       time.Duration
	}{
		{"wrong verifier", appsignin.ClientID, strings.Repeat("w", 43), appsignin.RedirectURI, 0},
		{"short verifier", appsignin.ClientID, "short", appsignin.RedirectURI, 0},
		{"verifier with a space", appsignin.ClientID, verifier + " ", appsignin.RedirectURI, 0},
		{"wrong redirect", appsignin.ClientID, verifier, "dev.kiff.tafs:/other", 0},
		{"wrong client", "someone-else", verifier, appsignin.RedirectURI, 0},
		{"after 60 seconds", appsignin.ClientID, verifier, appsignin.RedirectURI, appsignin.CodeTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, true)
			c := f.signIn()
			f.now = f.now.Add(tc.wait)
			if _, err := f.h.Redeem(context.Background(), tc.client, c, tc.verifier, tc.redirect); !errors.Is(err, appsignin.ErrBadGrant) {
				t.Fatalf("refused: %v", err)
			}
			// The wrong try spent it: the right one now fails too.
			f.now = f.now.Add(-tc.wait)
			if _, err := f.h.Redeem(context.Background(), appsignin.ClientID, c, verifier, appsignin.RedirectURI); !errors.Is(err, appsignin.ErrBadGrant) {
				t.Fatalf("after a wrong try: %v", err)
			}
		})
	}
	f := newFixture(t, true)
	c := f.signIn()
	f.now = f.now.Add(appsignin.CodeTTL - time.Second)
	if _, err := f.h.Redeem(context.Background(), appsignin.ClientID, c, verifier, appsignin.RedirectURI); err != nil {
		t.Fatalf("at 59 seconds: %v", err)
	}
	if _, err := f.h.Redeem(context.Background(), appsignin.ClientID, "", verifier, appsignin.RedirectURI); !errors.Is(err, appsignin.ErrBadGrant) {
		t.Fatalf("no code: %v", err)
	}
}

// Authorize sends anything wrong back to the app with the error and state,
// except a redirect that isn't the app's, which is never followed.
func TestAuthorizeRefusesWhatTheContractRefuses(t *testing.T) {
	f := newFixture(t, true)
	for _, tc := range []struct {
		name string
		over map[string]string
		want string
	}{
		{"no PKCE", map[string]string{"code_challenge": "", "code_challenge_method": ""}, "invalid_request"},
		{"plain", map[string]string{"code_challenge_method": "plain"}, "invalid_request"},
		{"a challenge that isn't S256's shape", map[string]string{"code_challenge": "short"}, "invalid_request"},
		{"no state", map[string]string{"state": ""}, "invalid_request"},
		{"a short state", map[string]string{"state": "abc"}, "invalid_request"},
		{"token response", map[string]string{"response_type": "token"}, "invalid_request"},
	} {
		code, loc := f.authorize(authorizeQuery(tc.over))
		if code != http.StatusFound || loc.Scheme != "dev.kiff.tafs" || loc.Query().Get("error") != tc.want {
			t.Fatalf("%s: %d %v", tc.name, code, loc)
		}
		if s := authorizeQuery(tc.over).Get("state"); loc.Query().Get("state") != s {
			t.Fatalf("%s: state %q, want %q", tc.name, loc.Query().Get("state"), s)
		}
	}
	for _, r := range []string{"", "dev.kiff.tafs:/oauth/", "https://evil.example/oauth", "dev.kiff.tafs://oauth"} {
		if code, loc := f.authorize(authorizeQuery(map[string]string{"redirect_uri": r})); code != http.StatusBadRequest || loc.String() != "" {
			t.Fatalf("redirect %q: %d %v", r, code, loc)
		}
	}
}

// Only the eight languages and a plain email pass to the sign-in page.
func TestAuthorizePassesOnlyKnownLocalesAndAnEmailHint(t *testing.T) {
	f := newFixture(t, true)
	_, loc := f.authorize(authorizeQuery(map[string]string{"ui_locales": "de", "login_hint": "ana@example.com"}))
	if loc.Query().Get("ui_locales") != "de" || loc.Query().Get("login_hint") != "ana@example.com" || !strings.HasPrefix(loc.Query().Get("request"), "ar_") {
		t.Fatalf("passed: %v", loc.Query())
	}
	_, loc = f.authorize(authorizeQuery(map[string]string{"ui_locales": "xx", "login_hint": "Ana <ana@example.com>"}))
	if loc.Query().Has("ui_locales") || loc.Query().Has("login_hint") {
		t.Fatalf("dropped: %v", loc.Query())
	}
}

// Off: authorize answers unsupported_client, approve isn't there, and nothing redeems.
func TestOffRefusesEverything(t *testing.T) {
	f := newFixture(t, false)
	code, loc := f.authorize(authorizeQuery(nil))
	if code != http.StatusFound || loc.Query().Get("error") != "unsupported_client" {
		t.Fatalf("authorize off: %d %v", code, loc)
	}
	if status, _ := f.call("/v1/auth/app/approve", &ana, `{"request":"ar_x"}`); status != http.StatusNotFound {
		t.Fatalf("approve off: %d", status)
	}
	if _, err := f.h.Redeem(context.Background(), appsignin.ClientID, "c", verifier, appsignin.RedirectURI); !errors.Is(err, appsignin.ErrOff) {
		t.Fatalf("redeem off: %v", err)
	}
}

// Approve needs a signed-in person; a request works once and lapses; deny sends access_denied.
func TestApproveAndDeny(t *testing.T) {
	f := newFixture(t, true)
	request := func() string {
		_, loc := f.authorize(authorizeQuery(nil))
		return loc.Query().Get("request")
	}
	id := request()
	for _, who := range []*auth.Identity{nil, {Subject: "k", Tenant: "t", Method: auth.MethodSession, APIKey: true}, {Subject: "s", Tenant: "", Method: auth.MethodSession}} {
		if status, _ := f.call("/v1/auth/app/approve", who, `{"request":"`+id+`"}`); status != http.StatusForbidden {
			t.Fatalf("approve as %+v: %d", who, status)
		}
	}
	if status, _ := f.call("/v1/auth/app/approve", &ana, `{"request":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("approve after refusals: %d", status)
	}
	if status, _ := f.call("/v1/auth/app/approve", &ana, `{"request":"`+id+`"}`); status != http.StatusGone {
		t.Fatalf("a used request: %d", status)
	}
	late := request()
	f.now = f.now.Add(appsignin.RequestTTL)
	if status, _ := f.call("/v1/auth/app/approve", &ana, `{"request":"`+late+`"}`); status != http.StatusGone {
		t.Fatalf("a lapsed request: %d", status)
	}
	status, out := f.call("/v1/auth/app/deny", &ana, `{"request":"`+request()+`"}`)
	back, _ := url.Parse(out["redirect_to"])
	if status != http.StatusOK || back.Query().Get("error") != "access_denied" || back.Query().Get("state") != "state-0123456789abcdef" || back.Query().Has("code") {
		t.Fatalf("deny: %d %v", status, back)
	}
	if status, _ := f.call("/v1/auth/app/approve", &ana, `{"request":"x","more":1}`); status != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d", status)
	}
}

// Approving needs a recent sign-in, as the device flow's approve does: the
// code mints a session that approves held calls.
func TestApproveNeedsARecentSignIn(t *testing.T) {
	f := &fixture{t: t, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	f.h = appsignin.New(true, page, appsignin.WithClock(func() time.Time { return f.now }),
		appsignin.WithStepUp(&auth.StepUp{Window: auth.StepUpWindow}))
	f.mux = http.NewServeMux()
	f.h.RegisterAuthenticated(f.mux)
	_, loc := f.authorize(authorizeQuery(nil))
	id := loc.Query().Get("request")
	old, recent := 11*time.Minute, 2*time.Minute
	stale := ana
	stale.FactorAge = &old
	if status, _ := f.call("/v1/auth/app/approve", &stale, `{"request":"`+id+`"}`); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("an 11-minute-old sign-in: %d", status)
	}
	fresh := ana
	fresh.FactorAge = &recent
	// The refusal didn't spend the request: the fresh sign-in approves it.
	if status, out := f.call("/v1/auth/app/approve", &fresh, `{"request":"`+id+`"}`); status != http.StatusOK || !strings.Contains(out["redirect_to"], "code=") {
		t.Fatalf("a 2-minute-old sign-in: %d %v", status, out)
	}
}
