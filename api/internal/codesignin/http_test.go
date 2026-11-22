package codesignin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
)

type stepUp struct{ ok bool }

func (s stepUp) Check(context.Context, auth.Identity) (int, error) {
	if s.ok {
		return 0, nil
	}
	return 0, auth.ErrReauthRequired
}

func serveCode(h *Handler, method, path, body string, id *auth.Identity) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.RegisterWeb(mux)
	h.RegisterAuthenticated(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The web's routes answer only the box: from outside they don't exist. The
// send answers the same for every address.
func TestTheWebsRoutesAnswerOnlyTheBoxAndTheSame(t *testing.T) {
	r := newRig(t)
	h := NewHandler(r.s, stepUp{}, nil)
	h.fromBox = func(*http.Request) bool { return false }
	for _, p := range []string{"/v1/auth/code/send", "/v1/auth/code/verify"} {
		if rec := serveCode(h, "POST", p, `{"email":"on@example.com","flow":"f","code":"123456"}`, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s from outside: %d", p, rec.Code)
		}
	}
	h.fromBox = func(*http.Request) bool { return true }
	var bodies []string
	for _, a := range []string{"on@example.com", "off@example.com", "nobody@example.com"} {
		rec := serveCode(h, "POST", "/v1/auth/code/send", `{"email":"`+a+`","flow":"f-`+a+`","ip":"198.51.100.7","lang":"de"}`, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: %d", a, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
		if a != "nobody@example.com" {
			<-r.done
		}
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ: %q", bodies)
	}
	code := r.lastCode("on@example.com")
	rec := serveCode(h, "POST", "/v1/auth/code/verify", `{"email":"on@example.com","flow":"f-on@example.com","code":"`+code+`","ip":"198.51.100.7"}`, nil)
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["ticket"] != "ticket-for-user_on" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	rec = serveCode(h, "POST", "/v1/auth/code/verify", `{"email":"nobody@example.com","flow":"f","code":"123456","ip":"198.51.100.7"}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code_wrong") {
		t.Errorf("an unknown address: %d %s", rec.Code, rec.Body)
	}
}

// Turning codes on needs a recent sign-in; turning them off doesn't. Without
// the key, codes can't be turned on.
func TestTurningCodesOnNeedsARecentSignIn(t *testing.T) {
	r := newRig(t)
	id := &auth.Identity{Subject: "user_off", Method: auth.MethodSession}
	h := NewHandler(r.s, stepUp{ok: false}, nil)
	if rec := serveCode(h, "PUT", "/v1/me/sign-in", `{"email_code":true}`, id); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "reauth_required") {
		t.Fatalf("on without a recent sign-in: %d %s", rec.Code, rec.Body)
	}
	if set, _ := r.store.Setting(context.Background(), "user_off"); set.EmailCode {
		t.Fatal("turned on anyway")
	}
	h = NewHandler(r.s, stepUp{ok: true}, nil)
	if rec := serveCode(h, "PUT", "/v1/me/sign-in", `{"email_code":true}`, id); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email_code":true`) {
		t.Fatalf("on: %d %s", rec.Code, rec.Body)
	}
	<-r.done
	h = NewHandler(r.s, stepUp{ok: false}, nil)
	if rec := serveCode(h, "PUT", "/v1/me/sign-in", `{"email_code":false}`, id); rec.Code != http.StatusOK {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}
	<-r.done
	if rec := serveCode(h, "GET", "/v1/me/sign-in", "", id); !strings.Contains(rec.Body.String(), `"email_code":false`) || !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Errorf("get: %s", rec.Body)
	}
	if rec := serveCode(h, "PUT", "/v1/me/sign-in", `{}`, id); rec.Code != http.StatusBadRequest {
		t.Errorf("no value: %d", rec.Code)
	}
	if rec := serveCode(h, "GET", "/v1/me/sign-in", "", &auth.Identity{Subject: "x", APIKey: true}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a key: %d", rec.Code)
	}
	none := NewHandler(nil, stepUp{ok: true}, nil)
	if rec := serveCode(none, "PUT", "/v1/me/sign-in", `{"email_code":true}`, id); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("on without the key: %d", rec.Code)
	}
	if rec := serveCode(none, "GET", "/v1/me/sign-in", "", id); !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Errorf("get without the key: %s", rec.Body)
	}
}

// A call carrying a key or a session never reaches the web's routes.
func TestTheWebsRoutesRefuseACredential(t *testing.T) {
	r := newRig(t)
	h := NewHandler(r.s, stepUp{}, nil)
	h.fromBox = func(*http.Request) bool { return true }
	mux := http.NewServeMux()
	h.RegisterWeb(mux)
	req := httptest.NewRequest("POST", "/v1/auth/code/send", strings.NewReader(`{"email":"on@example.com","flow":"f"}`))
	req.Header.Set("Authorization", "Bearer key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(r.m.all()) != 0 {
		t.Fatalf("with a credential: %d", rec.Code)
	}
}

// Turning codes on is refused to a person with a second factor.
func TestTurningCodesOnIsRefusedWithASecondFactor(t *testing.T) {
	r := newRig(t)
	r.p.two = map[string]bool{"user_off": true}
	h := NewHandler(r.s, stepUp{ok: true}, nil)
	rec := serveCode(h, "PUT", "/v1/me/sign-in", `{"email_code":true}`, &auth.Identity{Subject: "user_off", Method: auth.MethodSession})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "second_factor") {
		t.Fatalf("on with a second factor: %d %s", rec.Code, rec.Body)
	}
	if set, _ := r.store.Setting(context.Background(), "user_off"); set.EmailCode {
		t.Fatal("turned on anyway")
	}
}

// servePhone calls the person's setting as a phone's session does: through
// the real route policy, with a device key in the identity.
func servePhone(h *Handler, method, body string, id auth.Identity) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.RegisterAuthenticated(mux)
	req := httptest.NewRequest(method, "/v1/me/sign-in", strings.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	auth.DefaultRoutePolicy().Wrap(mux).ServeHTTP(rec, req)
	return rec
}

func phone(subject string) auth.Identity {
	return auth.Identity{Subject: subject, Method: auth.MethodDeveloper, DeviceKey: []byte("phone-key"), DeviceSessionID: "sess-phone"}
}

// A phone's session reads the person's own setting and turns codes off, and the
// person is told by email; it never turns them on (that needs a recent sign-in
// at kiff.dev, and a phone's session has none), even where a recent sign-in
// would pass. It touches only its own person's setting.
func TestAPhoneSessionReadsAndTurnsCodesOffButNeverOn(t *testing.T) {
	r := newRig(t)
	// A recent sign-in that would pass: the refusal is the device session's, not the check's.
	h := NewHandler(r.s, stepUp{ok: true}, nil)
	ctx := context.Background()

	// Read: codes are on for user_on, off for user_off.
	if rec := servePhone(h, "GET", "", phone("user_on")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email_code":true`) {
		t.Fatalf("a phone's read: %d %s", rec.Code, rec.Body)
	}
	if rec := servePhone(h, "GET", "", phone("user_off")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email_code":false`) {
		t.Fatalf("a phone's read of its own off: %d %s", rec.Code, rec.Body)
	}

	// Turning on from a phone: refused with the reason, and nothing changed.
	rec := servePhone(h, "PUT", `{"email_code":true}`, phone("user_off"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "turn_on_at_kiff") || !strings.Contains(rec.Body.String(), "kiff.dev") {
		t.Fatalf("a phone turning codes on: %d %s", rec.Code, rec.Body)
	}
	if set, _ := r.store.Setting(ctx, "user_off"); set.EmailCode {
		t.Fatal("a phone turned codes on")
	}
	if len(r.m.all()) != 0 {
		t.Fatalf("a refused turn-on sent mail: %v", r.m.all())
	}

	// Turning off from a phone works, and the person is told.
	if rec := servePhone(h, "PUT", `{"email_code":false}`, phone("user_on")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email_code":false`) {
		t.Fatalf("a phone turning codes off: %d %s", rec.Code, rec.Body)
	}
	<-r.done
	if set, _ := r.store.Setting(ctx, "user_on"); set.EmailCode {
		t.Fatal("codes are still on after a phone turned them off")
	}
	told := false
	for _, m := range r.m.all() {
		if m.kind == "changed" && !m.on && m.to == "on@example.com" {
			told = true
		}
	}
	if !told {
		t.Fatalf("the person was not told codes were turned off: %v", r.m.all())
	}
}

// A phone's session is its own person's: another person's setting is out of
// its reach, a session without a person gets nothing, and the phone's other
// limits are unchanged.
func TestAPhoneSessionReachesOnlyItsOwnPersonsSetting(t *testing.T) {
	r := newRig(t)
	h := NewHandler(r.s, stepUp{ok: true}, nil)
	ctx := context.Background()
	// user_off's phone turns codes off: user_on's codes stay on.
	if rec := servePhone(h, "PUT", `{"email_code":false}`, phone("user_off")); rec.Code != http.StatusOK {
		t.Fatalf("off for its own person: %d %s", rec.Code, rec.Body)
	}
	if set, _ := r.store.Setting(ctx, "user_on"); !set.EmailCode {
		t.Fatal("another person's phone turned this person's codes off")
	}
	// No person in the session: nothing is read or changed.
	for _, c := range [][2]string{{"GET", ""}, {"PUT", `{"email_code":false}`}} {
		if rec := servePhone(h, c[0], c[1], phone("")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s from a phone session with no person: %d %s", c[0], rec.Code, rec.Body)
		}
	}
	// What else the phone's session reaches is unchanged: a route outside its list is still refused.
	mux := http.NewServeMux()
	h.RegisterAuthenticated(mux)
	mux.HandleFunc("POST /v1/me/approver-grants", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest("POST", "/v1/me/approver-grants", strings.NewReader(`{}`))
	req = req.WithContext(auth.WithIdentity(req.Context(), phone("user_on")))
	rec := httptest.NewRecorder()
	auth.DefaultRoutePolicy().Wrap(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "phone_session_forbidden") {
		t.Errorf("a route outside the phone's list: %d %s", rec.Code, rec.Body)
	}
}

// A view-only session (the extension's, or a computer with no approval key) reads the
// setting but changes none: turning codes off or on is refused with view_only, and the
// stored setting is untouched.
func TestAViewOnlySessionReadsButChangesNoSetting(t *testing.T) {
	r := newRig(t)
	h := NewHandler(r.s, stepUp{ok: true}, nil)
	ctx := context.Background()
	view := phone("user_on")
	view.ApprovalMode = auth.ApprovalModeView
	if w := servePhone(h, http.MethodGet, "", view); w.Code != http.StatusOK {
		t.Fatalf("a view-only session reads: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"email_code":false}`, `{"email_code":true}`} {
		w := servePhone(h, http.MethodPut, body, view)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "view_only") {
			t.Fatalf("view-only %s: %d %s", body, w.Code, w.Body)
		}
	}
	if set, _ := r.store.Setting(ctx, "user_on"); !set.EmailCode {
		t.Fatal("a view-only session changed the setting")
	}
	soft := phone("user_on")
	soft.KeyStore = auth.KeyStoreSoftware
	if w := servePhone(h, http.MethodPut, `{"email_code":false}`, soft); w.Code != http.StatusForbidden {
		t.Fatalf("a software-key computer without a security key: %d", w.Code)
	}
}
