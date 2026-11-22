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
	h.internal = func(*http.Request) bool { return false }
	for _, p := range []string{"/v1/auth/code/send", "/v1/auth/code/verify"} {
		if rec := serveCode(h, "POST", p, `{"email":"on@example.com","flow":"f","code":"123456"}`, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s from outside: %d", p, rec.Code)
		}
	}
	h.internal = func(*http.Request) bool { return true }
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
	h.internal = func(*http.Request) bool { return true }
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
