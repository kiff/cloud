package handlers_test

import (
	"bytes"
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
	"github.com/kiff/cloud/api/internal/handlers"
)

var appVerifier = strings.Repeat("a", 43) + "~verifier"

type appRig struct {
	*deviceRig
	devices  *auth.MemoryDeviceStore
	app      *appsignin.Handler
	appMux   *http.ServeMux
	consents *appsignin.MemoryConsents
}

func newAppRig(t *testing.T, on bool) *appRig {
	t.Helper()
	r := &appRig{consents: &appsignin.MemoryConsents{}}
	r.app = appsignin.New(on, "https://app.kiff.test/sign-in/app", appsignin.WithClock(func() time.Time {
		if r.deviceRig == nil {
			return time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}))
	r.deviceRig, r.devices = devicesRig(t, "t_ana", "user_ana", handlers.WithDeviceAppSignIn(r.app, r.consents))
	r.appMux = http.NewServeMux()
	r.app.RegisterAuthenticated(r.appMux)
	return r
}

// code signs Ana in inside the sheet and returns the code the app gets.
func (r *appRig) code(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(appVerifier))
	q := url.Values{"response_type": {"code"}, "client_id": {appsignin.ClientID}, "redirect_uri": {appsignin.RedirectURI},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {"state-0123456789abcdef"}}
	w := httptest.NewRecorder()
	r.app.Authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	loc, _ := url.Parse(w.Header().Get("Location"))
	req := httptest.NewRequest("POST", "/v1/auth/app/approve", strings.NewReader(`{"request":"`+loc.Query().Get("request")+`"}`))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "user_ana", Tenant: "t_ana", Method: auth.MethodSession}))
	aw := httptest.NewRecorder()
	r.appMux.ServeHTTP(aw, req)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(aw.Body.Bytes(), &out)
	back, _ := url.Parse(out.RedirectTo)
	if back == nil || back.Query().Get("code") == "" {
		t.Fatalf("no code: %d %s", aw.Code, aw.Body.String())
	}
	return back.Query().Get("code")
}

func (r *appRig) token(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	r.pub.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/auth/app/token", bytes.NewReader(b)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func appBody(code string, over map[string]any) map[string]any {
	b := map[string]any{"client_id": appsignin.ClientID, "code": code, "code_verifier": appVerifier, "redirect_uri": appsignin.RedirectURI,
		"terms_version": appsignin.DefaultTermsVersion, "device_name": "Ana's iPhone"}
	for k, v := range over {
		if v == nil {
			delete(b, k)
		} else {
			b[k] = v
		}
	}
	return b
}

// A phone signs in inside the app: the code and its verifier mint the device
// flow's own session, keep the device with its keys, record the terms ticked
// with KIFF's clock, and answer the device flow's DeviceToken. The code works
// once.
func TestAnAppSignInMintsTheDeviceFlowsSessionAndRecordsTheTerms(t *testing.T) {
	r := newAppRig(t, true)
	devDER, devB64 := newKey(t)
	_, apprB64 := newKey(t)
	c := r.code(t)
	status, out := r.token(t, appBody(c, map[string]any{"public_key": devB64, "approval_public_key": apprB64, "platform": "ios", "kind": "iphone"}))
	if status != http.StatusOK || out["token_type"] != "Bearer" || out["tenant"] != "t_ana" || out["subject"] != "user_ana" || out["approval_mode"] != auth.ApprovalModeKey {
		t.Fatalf("token: %d %v", status, out)
	}
	parsed, err := auth.ParseDevSessionToken(out["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := r.sessions.GetByHash(context.Background(), parsed.Hash, r.now)
	if err != nil || !bytes.Equal(sess.DevicePublicKey, devDER) || sess.DeviceID == "" || sess.Label != "Ana's iPhone" {
		t.Fatalf("session: %+v %v", sess, err)
	}
	dev, err := r.devices.FindByKey(context.Background(), "t_ana", "user_ana", devDER)
	if err != nil || dev.ID != sess.DeviceID || !dev.HasApprovalKey || dev.Platform != "ios" {
		t.Fatalf("device: %+v %v", dev, err)
	}
	got := r.consents.All()
	if len(got) != 1 || got[0] != (appsignin.Consent{Tenant: "t_ana", Subject: "user_ana", TermsVersion: appsignin.DefaultTermsVersion,
		Client: appsignin.ClientID, DeviceID: dev.ID, SessionID: sess.ID, AcceptedAt: r.now}) {
		t.Fatalf("consent: %+v", got)
	}
	if status, out := r.token(t, appBody(c, map[string]any{"public_key": devB64, "approval_public_key": apprB64})); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("the code again: %d %v", status, out)
	}
}

// What the app can fix is refused before the code is spent: old terms, a
// missing version, a device field the device flow refuses. The same code then
// works with them fixed.
func TestAFixableRefusalLeavesTheCode(t *testing.T) {
	r := newAppRig(t, true)
	devB64 := func() string { _, b := newKey(t); return b }
	c := r.code(t)
	pub := devB64()
	for _, tc := range []struct {
		name   string
		over   map[string]any
		status int
		err    string
	}{
		{"old terms", map[string]any{"terms_version": "2026-01-01"}, http.StatusConflict, "terms_outdated"},
		{"no terms", map[string]any{"terms_version": nil}, http.StatusBadRequest, "invalid_request"},
		{"the approval key is the device key", map[string]any{"public_key": pub, "approval_public_key": pub}, http.StatusBadRequest, "invalid_approval_public_key"},
		{"an unknown platform", map[string]any{"public_key": pub, "platform": "beos"}, http.StatusBadRequest, "invalid_platform"},
		{"a computer approving with its device key", map[string]any{"public_key": pub, "platform": "macos"}, http.StatusBadRequest, "approval_required"},
	} {
		if status, out := r.token(t, appBody(c, tc.over)); status != tc.status || out["error"] != tc.err {
			t.Fatalf("%s: %d %v", tc.name, status, out)
		}
		if tc.err == "terms_outdated" {
			if status, out := r.token(t, appBody(c, tc.over)); out["terms_version"] != appsignin.DefaultTermsVersion {
				t.Fatalf("terms_outdated names the version: %d %v", status, out)
			}
		}
	}
	if status, out := r.token(t, appBody(c, map[string]any{"public_key": pub, "approval_public_key": devB64()})); status != http.StatusOK {
		t.Fatalf("fixed: %d %v", status, out)
	}
	if len(r.consents.All()) != 1 {
		t.Fatalf("consents: %v", r.consents.All())
	}
}

// A code with a wrong verifier, redirect or client mints nothing and records nothing.
func TestAWrongCodeMintsNothing(t *testing.T) {
	r := newAppRig(t, true)
	for _, over := range []map[string]any{
		{"code_verifier": strings.Repeat("b", 43)},
		{"redirect_uri": "dev.kiff.tafs:/elsewhere"},
		{"client_id": "someone-else"},
		{"code": "not-a-code"},
	} {
		if status, out := r.token(t, appBody(r.code(t), over)); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
			t.Fatalf("%v: %d %v", over, status, out)
		}
	}
	if len(r.consents.All()) != 0 {
		t.Fatalf("consents: %v", r.consents.All())
	}
}

// A consent that isn't recorded undoes the session and the device it made:
// no one is signed in without the terms on record.
func TestNoConsentNoSession(t *testing.T) {
	r := newAppRig(t, true)
	r.consents.Fail = errors.New("database down")
	devDER, devB64 := newKey(t)
	_, apprB64 := newKey(t)
	status, out := r.token(t, appBody(r.code(t), map[string]any{"public_key": devB64, "approval_public_key": apprB64}))
	if status != http.StatusInternalServerError || out["access_token"] != nil {
		t.Fatalf("token: %d %v", status, out)
	}
	if dev, err := r.devices.FindByKey(context.Background(), "t_ana", "user_ana", devDER); err == nil && dev.RevokedAt == nil {
		t.Fatalf("the device stayed: %+v", dev)
	}
	if live, err := r.sessions.ListActiveForSubject(context.Background(), "user_ana", r.now); err != nil || len(live) != 0 {
		t.Fatalf("live sessions: %v %v", live, err)
	}
}

// A computer signing in once the person has a phone that approves is view-only
// until it pairs, as in the device flow.
func TestASecondDeviceGetsTheDeviceFlowsRules(t *testing.T) {
	r := newAppRig(t, true)
	_, phone := newKey(t)
	_, phoneAppr := newKey(t)
	if status, out := r.token(t, appBody(r.code(t), map[string]any{"public_key": phone, "approval_public_key": phoneAppr, "platform": "ios", "kind": "iphone"})); status != http.StatusOK {
		t.Fatalf("phone: %d %v", status, out)
	}
	_, mac := newKey(t)
	_, macAppr := newKey(t)
	status, out := r.token(t, appBody(r.code(t), map[string]any{"public_key": mac, "approval_public_key": macAppr, "platform": "macos", "kind": "other", "device_name": "Ana's Mac"}))
	if status != http.StatusOK || out["approval_mode"] != auth.ApprovalModeView {
		t.Fatalf("a computer after a phone: %d %v", status, out)
	}
}

// Off: the token answers unsupported_client and the terms aren't served.
func TestAppTokenOff(t *testing.T) {
	r := newAppRig(t, false)
	if status, out := r.token(t, appBody("c", nil)); status != http.StatusBadRequest || out["error"] != "unsupported_client" {
		t.Fatalf("token off: %d %v", status, out)
	}
	w := httptest.NewRecorder()
	r.pub.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/auth/app/terms", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("terms off: %d", w.Code)
	}
	on := newAppRig(t, true)
	w = httptest.NewRecorder()
	on.pub.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/auth/app/terms", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"terms_version":"`+appsignin.DefaultTermsVersion+`"`) {
		t.Fatalf("terms on: %d %s", w.Code, w.Body.String())
	}
	// Without the option, no app route at all.
	plain := newDeviceRig(t, "t_ana", "user_ana")
	w = httptest.NewRecorder()
	plain.pub.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/auth/app/token", strings.NewReader("{}")))
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("no app sign-in: %d", w.Code)
	}
}

// A code used again after it minted a session ends that session and the
// device it made (RFC 6749 §4.1.2): whoever replays it gets invalid_grant, and
// the first sign-in is signed out too.
func TestAReplayedCodeEndsTheSessionItMade(t *testing.T) {
	r := newAppRig(t, true)
	devDER, devB64 := newKey(t)
	_, apprB64 := newKey(t)
	c := r.code(t)
	body := appBody(c, map[string]any{"public_key": devB64, "approval_public_key": apprB64, "platform": "ios", "kind": "iphone"})
	status, out := r.token(t, body)
	if status != http.StatusOK {
		t.Fatalf("first: %d %v", status, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	if _, err := r.sessions.GetByHash(context.Background(), parsed.Hash, r.now); err != nil {
		t.Fatalf("the session before the replay: %v", err)
	}
	if status, out := r.token(t, body); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("replay: %d %v", status, out)
	}
	if _, err := r.sessions.GetByHash(context.Background(), parsed.Hash, r.now); err == nil {
		t.Fatal("the replayed code's session still works")
	}
	if dev, err := r.devices.FindByKey(context.Background(), "t_ana", "user_ana", devDER); err == nil && dev.RevokedAt == nil {
		t.Fatalf("the replayed code's device stayed: %+v", dev)
	}
}

// Failed token requests from one address are limited: after 20 in ten
// minutes it's slow_down, even with a good code, and another address isn't.
func TestFailedTokenRequestsAreLimitedPerAddress(t *testing.T) {
	r := newAppRig(t, true)
	post := func(ip string, body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/app/token", bytes.NewReader(b))
		req.Header.Set("X-Forwarded-For", "203.0.113.9, "+ip)
		w := httptest.NewRecorder()
		r.pub.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for i := range 20 {
		if status, out := post("198.51.100.1", appBody("guess", nil)); out["error"] != "invalid_grant" {
			t.Fatalf("try %d: %d %v", i, status, out)
		}
	}
	if status, out := post("198.51.100.1", appBody(r.code(t), nil)); status != http.StatusTooManyRequests || out["error"] != "slow_down" {
		t.Fatalf("21st: %d %v", status, out)
	}
	if status, out := post("198.51.100.2", appBody(r.code(t), nil)); status != http.StatusOK {
		t.Fatalf("another address: %d %v", status, out)
	}
	r.advance(10 * time.Minute)
	if status, out := post("198.51.100.1", appBody(r.code(t), nil)); status != http.StatusOK {
		t.Fatalf("after the window: %d %v", status, out)
	}
}
