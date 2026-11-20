package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/testdb"
	"github.com/kiff/kiff/pkg/kiff/actor"
)

// The tafs apps' sign-in end to end, through the API's own router (K5 of the
// in-app sign-in, api/internal/appsignin/README.md): authorize, the sign-in
// page's approve as the signed-in person, the token exchange, then a request
// with the session it minted. The page itself is the web's (/sign-in/app); here
// the test plays it, with the person's session. Against a local KIFF: the API's
// own schema on a Postgres (KIFF_TEST_POSTGRES_URL, as CI runs it), since
// /oauth/authorize is served only with a database; skipped without one. Not
// parallel: the switch is an environment variable.

const (
	e2eVerifier = "e2e-verifier-0123456789abcdefghijklmnopqrstuvwxyz"
	e2eState    = "state-0123456789abcdef"
	e2eWebBase  = "https://app.kiff.test"
)

// appSignInRouter is the API with app sign-in switched on (or off), on the test
// database with the API's own schema, and Ana signed in on the web as "ana-web".
func appSignInRouter(t *testing.T, on bool) http.Handler {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set: the app sign-in end-to-end test needs KIFF's database")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, closePool, err := openPool(context.Background(), url, logger)
	if err != nil {
		t.Fatalf("the API's database: %v", err)
	}
	t.Cleanup(closePool)
	if on {
		t.Setenv(envAppSignIn, "on")
	} else {
		t.Setenv(envAppSignIn, "")
	}
	resolver := auth.NewMapResolver()
	resolver.Set("ana-web", actor.Actor{ID: "user_ana", Type: actor.TypeHuman, DisplayName: "Ana", Roles: []string{"admin"}}, "t_ana")
	return newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receipt.NewMemoryStore(),
		resolver:         resolver,
		devSessionStore:  auth.NewMemoryDeveloperSessionStore(),
		deviceStore:      auth.NewMemoryDeviceStore(),
		appBaseURL:       e2eWebBase,
		pool:             pool,
		logger:           logger,
	})
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeQuery(over map[string]string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {appsignin.ClientID}, "redirect_uri": {appsignin.RedirectURI},
		"code_challenge": {challengeOf(e2eVerifier)}, "code_challenge_method": {"S256"}, "state": {e2eState}}
	for k, v := range over {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	return "/oauth/authorize?" + q.Encode()
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// authorize starts a sign-in and returns the request id the page gets.
func authorize(t *testing.T, h http.Handler) string {
	t.Helper()
	w := do(h, httptest.NewRequest(http.MethodGet, authorizeQuery(nil), nil))
	loc, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), e2eWebBase+"/sign-in/app?") || loc.Query().Get("request") == "" {
		t.Fatalf("authorize: %d → %q (%s)", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	return loc.Query().Get("request")
}

// approve is the page's approve as Ana, and the app's redirect it answers.
func approve(t *testing.T, h http.Handler, request string) *url.URL {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/app/approve", strings.NewReader(`{"request":"`+request+`"}`))
	r.Header.Set("Authorization", "Bearer ana-web")
	r.Header.Set("Content-Type", "application/json")
	w := do(h, r)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	back, err := url.Parse(out.RedirectTo)
	if w.Code != http.StatusOK || err != nil {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	return back
}

func exchange(t *testing.T, h http.Handler, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/app/token", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := do(h, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func tokenBody(code string, over map[string]any) map[string]any {
	b := map[string]any{"client_id": appsignin.ClientID, "code": code, "code_verifier": e2eVerifier, "redirect_uri": appsignin.RedirectURI,
		"terms_version": appsignin.DefaultTermsVersion, "device_name": "Ana's iPhone"}
	for k, v := range over {
		b[k] = v
	}
	return b
}

// asSession calls an authenticated route with bearer: 404 (no such receipt) is
// a request the API let in, 401 one it refused.
func asSession(h http.Handler, bearer string) int {
	r := httptest.NewRequest(http.MethodGet, "/v1/receipts/trace-missing", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	return do(h, r).Code
}

func TestAppSignInEndToEnd(t *testing.T) {
	h := appSignInRouter(t, true)

	// The app opens the sheet: authorize keeps the request and sends the sheet to the web's page.
	request := authorize(t, h)
	// The page, once Ana is signed in there, approves: the sheet goes back to the app with a code and its state.
	back := approve(t, h, request)
	if !strings.HasPrefix(back.String(), appsignin.RedirectURI+"?") || back.Query().Get("state") != e2eState || back.Query().Get("code") == "" {
		t.Fatalf("the app's redirect: %q", back)
	}
	code := back.Query().Get("code")

	// Before the exchange, the code is no session.
	if got := asSession(h, code); got != http.StatusUnauthorized {
		t.Fatalf("the code itself was let in: %d", got)
	}
	// The app exchanges it, with its verifier and the terms it ticked: the device flow's own session.
	status, tok := exchange(t, h, tokenBody(code, nil))
	access, _ := tok["access_token"].(string)
	if status != http.StatusOK || access == "" || tok["subject"] != "user_ana" || tok["tenant"] != "t_ana" || tok["token_type"] != "Bearer" {
		t.Fatalf("token: %d %v", status, tok)
	}
	// The session it minted is Ana's, and the API lets it in.
	if got := asSession(h, access); got != http.StatusNotFound {
		t.Fatalf("the minted session: %d, want the route's own 404", got)
	}
	// Negative: a made-up bearer isn't.
	if got := asSession(h, "not-a-session"); got != http.StatusUnauthorized {
		t.Fatalf("a made-up bearer: %d", got)
	}

	// The code works once: used again, it's invalid_grant, and the session its first use made is ended
	// (RFC 6749 §4.1.2; the contract's "a replay ends what the code made").
	if status, out := exchange(t, h, tokenBody(code, nil)); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("a reused code: %d %v", status, out)
	}
	if got := asSession(h, access); got != http.StatusUnauthorized {
		t.Fatalf("the session a replayed code made still works: %d", got)
	}
	// The request was taken once: approving it again mints nothing.
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/app/approve", strings.NewReader(`{"request":"`+request+`"}`))
	r.Header.Set("Authorization", "Bearer ana-web")
	if w := do(h, r); w.Code != http.StatusGone {
		t.Fatalf("a second approve: %d %s", w.Code, w.Body.String())
	}
}

func TestAppSignInEndToEndRefusals(t *testing.T) {
	h := appSignInRouter(t, true)

	// A wrong verifier is invalid_grant, and it spends the code: the right one after it gets nothing.
	code := approve(t, h, authorize(t, h)).Query().Get("code")
	if status, out := exchange(t, h, tokenBody(code, map[string]any{"code_verifier": strings.Repeat("b", 43)})); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("a wrong verifier: %d %v", status, out)
	}
	if status, out := exchange(t, h, tokenBody(code, nil)); status != http.StatusBadRequest || out["error"] != "invalid_grant" || out["access_token"] != nil {
		t.Fatalf("the right verifier after a wrong one: %d %v", status, out)
	}

	// A wrong redirect at authorize is never followed: a page, no Location.
	for _, bad := range []string{"https://evil.example/cb", "dev.kiff.tafs:/other", "dev.kiff.tafs:/oauth/../x"} {
		w := do(h, httptest.NewRequest(http.MethodGet, authorizeQuery(map[string]string{"redirect_uri": bad}), nil))
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Errorf("redirect %q: %d → %q", bad, w.Code, w.Header().Get("Location"))
		}
	}
	// Plain PKCE, or none, goes back to the app as invalid_request, with the state echoed.
	for name, over := range map[string]map[string]string{
		"plain":        {"code_challenge_method": "plain", "code_challenge": e2eVerifier},
		"no challenge": {"code_challenge": "", "code_challenge_method": ""},
	} {
		w := do(h, httptest.NewRequest(http.MethodGet, authorizeQuery(over), nil))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || loc == nil || loc.Query().Get("error") != "invalid_request" || loc.Query().Get("state") != e2eState || loc.Query().Get("code") != "" {
			t.Errorf("%s: %d → %q", name, w.Code, w.Header().Get("Location"))
		}
	}

	// Approve needs a signed-in person: with no session, nothing.
	request := authorize(t, h)
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/app/approve", strings.NewReader(`{"request":"`+request+`"}`))
	if w := do(h, r); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("approve with no session: %d %s", w.Code, w.Body.String())
	}
	// Cancel on the page: the sheet goes back to the app with access_denied and the state.
	r = httptest.NewRequest(http.MethodPost, "/v1/auth/app/deny", strings.NewReader(`{"request":"`+request+`"}`))
	r.Header.Set("Authorization", "Bearer ana-web")
	w := do(h, r)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	denied, _ := url.Parse(out.RedirectTo)
	if w.Code != http.StatusOK || denied == nil || denied.Query().Get("error") != "access_denied" || denied.Query().Get("state") != e2eState || denied.Query().Get("code") != "" {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
}

func TestAppSignInEndToEndOff(t *testing.T) {
	h := appSignInRouter(t, false)
	// Off, authorize answers the app unsupported_client, with its state, and keeps nothing.
	w := do(h, httptest.NewRequest(http.MethodGet, authorizeQuery(nil), nil))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc == nil || loc.Query().Get("error") != "unsupported_client" || loc.Query().Get("state") != e2eState {
		t.Fatalf("authorize off: %d → %q", w.Code, w.Header().Get("Location"))
	}
	// The token route says the same, never a bare 404.
	if status, out := exchange(t, h, tokenBody("any-code", nil)); status != http.StatusBadRequest || out["error"] != "unsupported_client" {
		t.Fatalf("token off: %d %v", status, out)
	}
	// Approve and deny aren't there.
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/app/approve", strings.NewReader(`{"request":"x"}`))
	r.Header.Set("Authorization", "Bearer ana-web")
	if w := do(h, r); w.Code != http.StatusNotFound {
		t.Fatalf("approve off: %d", w.Code)
	}
}
