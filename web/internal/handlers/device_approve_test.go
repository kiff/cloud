package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/session"
)

func deviceTestSession() session.Session {
	return session.Session{Subject: "u1", Bearer: "tok", ExpiresAt: time.Now().Add(time.Hour)}
}

// deviceApproveAPI builds an apiclient.Client pointed at a fake API
// serving POST /v1/auth/device/approve with the given status/body, and
// the lookup of BCDF-GHJK (a device without a key).
func deviceApproveAPI(t *testing.T, status int, body string) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/auth/device/pending" && r.URL.Query().Get("user_code") == "BCDF-GHJK":
			_, _ = w.Write([]byte(`{"user_code":"BCDF-GHJK","device_name":"cli","has_key":false}`))
		case r.URL.Path == "/v1/auth/device/approve":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	return c
}

// devicePost posts the approval form.
func devicePost(t *testing.T, mux *http.ServeMux, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDeviceApprove_ShowPrefillsCode(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusOK, "{}"), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/auth/device?code=BCDF-GHJK", nil)
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "BCDF-GHJK") {
		t.Errorf("prefilled code missing from form:\n%s", body)
	}
	if !strings.Contains(body, "Approve this device") {
		t.Errorf("approval heading missing")
	}
}

func TestDeviceApprove_ShowRedirectsWhenNoSession(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusOK, "{}"), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/auth/device", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/sign-in") {
		t.Fatalf("want redirect to /sign-in; got %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDeviceApprove_PostApproves(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusOK, `{"status":"approved"}`), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := devicePost(t, mux, "user_code=BCDF-GHJK&key_fingerprint=&reviewed=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Device approved") {
		t.Errorf("expected approved state:\n%s", rec.Body.String())
	}
}

func TestDeviceApprove_PostEmptyCode(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusOK, "{}"), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "enter the code") {
		t.Errorf("expected empty-code prompt:\n%s", rec.Body.String())
	}
}

// Approving a device needs a recent sign-in. When the API refuses one,
// nothing is approved and the person is sent to sign in again, back to the
// same code. Back from signing in, a refusal says why instead of sending
// them round again.
func TestDeviceApprove_AnOldSignInIsAskedToSignInAgain(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusForbidden, `{"error":"reauth_required","message":"this change needs a recent sign-in"}`), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := devicePost(t, mux, "user_code=BCDF-GHJK&key_fingerprint=&reviewed=1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != deviceReauthURL("BCDF-GHJK", "en") {
		t.Fatalf("status = %d, location %q: want a redirect to sign in again", rec.Code, rec.Header().Get("Location"))
	}
	if deviceReauthURL("BCDF-GHJK", "en") != "/sign-out?next=%2Fauth%2Fdevice%3Fcode%3DBCDF-GHJK%26signed_in%3D1" {
		t.Errorf("sign in again goes to %q", deviceReauthURL("BCDF-GHJK", "en"))
	}

	rec = devicePost(t, mux, "user_code=BCDF-GHJK&key_fingerprint=&reviewed=1&signed_in=1")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "Device approved") {
		t.Fatalf("status = %d, approved without a recent sign-in:\n%s", rec.Code, body)
	}
	if !strings.Contains(body, "needs a recent sign-in") {
		t.Errorf("expected the recent sign-in message:\n%s", body)
	}
}

// The page asks for the recent sign-in before it offers Approve, as the API
// says when the code is looked up. Once only: back from signing in, it
// shows the device.
func TestDeviceApprove_SignsInAgainBeforeOfferingApprove(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device/pending" {
			_, _ = w.Write([]byte(`{"user_code":"BCDF-GHJK","device_name":"tafs on Android","has_key":false,"recent_sign_in":false}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	api, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withSession(req.Context(), deviceTestSession()))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/auth/device?code=BCDF-GHJK"); rec.Code != http.StatusFound || rec.Header().Get("Location") != deviceReauthURL("BCDF-GHJK", "en") {
		t.Fatalf("status = %d, location %q: want to sign in again first", rec.Code, rec.Header().Get("Location"))
	}
	rec := get("/auth/device?code=BCDF-GHJK&signed_in=1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "tafs on Android") {
		t.Fatalf("back from signing in, the page should show the device: %d\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="signed_in" value="1"`) {
		t.Error("approving after signing in again should not send the person round again")
	}
}

func TestDeviceApprove_PostUnknownCode(t *testing.T) {
	t.Parallel()
	h := NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusNotFound, "nope"), nil, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := devicePost(t, mux, "user_code=NOPE-NOPE&reviewed=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown or has expired") {
		t.Errorf("expected unknown-code message:\n%s", rec.Body.String())
	}
}

// A code opened again after it was approved shows as approved, not as
// expired (kiff/tafs#58), and nothing is approved twice.
func TestDeviceApprove_AnApprovedCodeSaysSo(t *testing.T) {
	t.Parallel()
	var approvals atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/pending":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"this code was already approved"}`))
		case "/v1/auth/device/approve":
			approvals.Add(1)
			w.WriteHeader(http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api, _ := apiclient.NewSessionAware(srv.URL, 0)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/auth/device?code=BCDF-GHJK", nil)
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Device approved") || strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("an approved code's page:\n%s", rec.Body.String())
	}
	if post := devicePost(t, mux, "user_code=BCDF-GHJK&reviewed=1"); !strings.Contains(post.Body.String(), "Device approved") {
		t.Fatalf("posting an approved code:\n%s", post.Body.String())
	}
	if n := approvals.Load(); n != 0 {
		t.Fatalf("approved %d times", n)
	}
}

// phoneAPI is a fake API with two phones' codes, each with its key, that
// counts approvals.
type phoneAPI struct {
	approvals atomic.Int32
	approved  atomic.Value // the last code approved
	tenant    atomic.Value // the account it was approved in
}

func newPhoneAPI(t *testing.T) (*phoneAPI, *apiclient.Client) {
	t.Helper()
	f := &phoneAPI{}
	keys := map[string]string{"BCDF-GHJK": "3f2a 91c0 7b1e 0d44", "MNPQ-RSTV": "88c1 0a7e 52d9 e310"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/pending":
			code := r.URL.Query().Get("user_code")
			if code == "WXYZ-BCDF" {
				_, _ = w.Write([]byte(`{"user_code":"WXYZ-BCDF","device_name":"iPhone de Ana","has_key":true,"key_fingerprint":"3f2a 91c0 7b1e 0d44",` +
					`"relink":true,"approval_key_fingerprint":"77aa 0b1c 2d3e 4f50","tenant":"t2"}`))
				return
			}
			fp, ok := keys[code]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"user_code":"` + code + `","device_name":"tafs on iPhone","has_key":true,"key_fingerprint":"` + fp + `"}`))
		case "/v1/auth/device/approve":
			var body struct {
				UserCode string `json:"user_code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.approvals.Add(1)
			f.approved.Store(body.UserCode)
			f.tenant.Store(r.Header.Get(apiclient.TenantHeader))
			_, _ = w.Write([]byte(`{"status":"approved"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api, _ := apiclient.NewSessionAware(srv.URL, 0)
	return f, api
}

// A phone that sent its signing key is named on the approval page with
// the key's fingerprint, and the approved page names it too.
func TestDeviceApprove_ShowsThePhoneAndItsKey(t *testing.T) {
	t.Parallel()
	f, api := newPhoneAPI(t)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/auth/device?code=BCDF-GHJK", nil)
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	for _, want := range []string{"tafs on iPhone", "3f2a 91c0 7b1e 0d44", "check that it matches",
		`name="key_fingerprint" value="3f2a 91c0 7b1e 0d44"`, `name="reviewed" value="1"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("approval page lacks %q", want)
		}
	}
	if strings.Contains(rec.Body.String(), `id="user_code"`) {
		t.Error("a reviewed code can still be edited")
	}
	if n := f.approvals.Load(); n != 0 {
		t.Fatalf("looking at a code approved it (%d)", n)
	}

	rec = devicePost(t, mux, "user_code=BCDF-GHJK&key_fingerprint=3f2a+91c0+7b1e+0d44&reviewed=1")
	if !strings.Contains(rec.Body.String(), "tafs on iPhone is signed in") {
		t.Errorf("approved page does not name the phone:\n%s", rec.Body.String())
	}
	if n, code := f.approvals.Load(), f.approved.Load(); n != 1 || code != "BCDF-GHJK" {
		t.Errorf("approvals = %d (%v), want 1 of BCDF-GHJK", n, code)
	}

	// Signing in on the way keeps the code.
	anon := httptest.NewRecorder()
	mux.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/auth/device?code=BCDF-GHJK", nil))
	if loc := anon.Header().Get("Location"); loc != "/sign-in?next=%2Fauth%2Fdevice%3Fcode%3DBCDF-GHJK" {
		t.Errorf("sign-in redirect = %q", loc)
	}
}

// A code is approved only after its own key was shown: a code typed and
// posted straight away, or a reviewed page whose code was swapped for
// another, gets the review of the code posted, and no approval.
func TestDeviceApprove_NeverApprovesAnUnreviewedCode(t *testing.T) {
	t.Parallel()
	f, api := newPhoneAPI(t)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)

	for name, form := range map[string]string{
		"typed and posted":          "user_code=MNPQ-RSTV",
		"swapped after review":      "user_code=MNPQ-RSTV&key_fingerprint=3f2a+91c0+7b1e+0d44&reviewed=1",
		"reviewed flag without key": "user_code=MNPQ-RSTV&reviewed=1",
	} {
		rec := devicePost(t, mux, form)
		if n := f.approvals.Load(); n != 0 {
			t.Fatalf("%s: approved before its key was shown", name)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "88c1 0a7e 52d9 e310") || !strings.Contains(body, "Check the device and its key below") {
			t.Errorf("%s: the page does not show the posted code's key:\n%s", name, body)
		}
	}
	// The review it showed approves that code.
	devicePost(t, mux, "user_code=MNPQ-RSTV&key_fingerprint=88c1+0a7e+52d9+e310&reviewed=1")
	if n, code := f.approvals.Load(), f.approved.Load(); n != 1 || code != "MNPQ-RSTV" {
		t.Errorf("approvals = %d (%v), want 1 of MNPQ-RSTV", n, code)
	}
}

// Typing a code is a lookup (a GET), never an approval.
func TestDeviceApprove_EntryFormLooksUp(t *testing.T) {
	t.Parallel()
	_, api := newPhoneAPI(t)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/auth/device", nil)
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `method="get"`) || !strings.Contains(body, `name="code"`) || strings.Contains(body, `method="post"`) {
		t.Errorf("the entry form should look the code up:\n%s", body)
	}
}

// A person on two accounts chooses which one the device signs in to,
// by name and role; only one of their own is accepted.
func TestDeviceApprove_AskWhichAccount(t *testing.T) {
	t.Parallel()
	f, api := newPhoneAPI(t)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)
	two := components.AccountSwitch{Accounts: []components.AccountOption{
		{TenantID: "t1", Label: "ana@example.com", Role: "admin", Current: true},
		{TenantID: "t2", Label: "Kiff Agents", Role: "viewer"},
	}}
	with := func(r *http.Request) *http.Request {
		ctx := components.WithAccountSwitch(withSession(r.Context(), deviceTestSession()), two)
		return r.WithContext(ctx)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, with(httptest.NewRequest(http.MethodGet, "/auth/device?code=BCDF-GHJK", nil)))
	for _, want := range []string{"Sign it in to", `name="tenant" value="t1" checked`, `name="tenant" value="t2"`,
		"Kiff Agents · Viewer", "consent-brand"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("approval page lacks %q", want)
		}
	}

	post := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, with(req))
		return rec
	}
	const reviewed = "user_code=BCDF-GHJK&key_fingerprint=3f2a+91c0+7b1e+0d44&reviewed=1"
	if rec := post(reviewed + "&tenant=t9"); !strings.Contains(rec.Body.String(), "Choose one of your accounts") || f.approvals.Load() != 0 {
		t.Fatalf("approved into an account not theirs (%d)", f.approvals.Load())
	}
	post(reviewed + "&tenant=t2")
	if n, tenant := f.approvals.Load(), f.tenant.Load(); n != 1 || tenant != "t2" {
		t.Fatalf("approvals = %d in %v, want 1 in t2", n, tenant)
	}
}

// A phone that asks to approve again with a new key (its faces or
// fingerprints changed) is named, with a warning to approve only with it
// in hand, and approved on its own account: no account to choose.
func TestDeviceApprove_ARelinkSaysWhichPhoneAndStaysOnItsAccount(t *testing.T) {
	t.Parallel()
	f, api := newPhoneAPI(t)
	mux := http.NewServeMux()
	NewDeviceApproveHandler(api, nil, nil, nil).Register(mux)
	two := components.AccountSwitch{Accounts: []components.AccountOption{
		{TenantID: "t1", Label: "ana@example.com", Role: "admin", Current: true},
		{TenantID: "t2", Label: "Kiff Agents", Role: "admin"},
	}}
	with := func(r *http.Request) *http.Request {
		return r.WithContext(components.WithAccountSwitch(withSession(r.Context(), deviceTestSession()), two))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, with(httptest.NewRequest(http.MethodGet, "/auth/device?code=WXYZ-BCDF", nil)))
	page := rec.Body.String()
	for _, want := range []string{"Let iPhone de Ana approve again", "Only approve this if iPhone de Ana is in your hands now.",
		"77aa 0b1c 2d3e 4f50", `name="tenant" value="t2"`, "Approve again"} {
		if !strings.Contains(page, want) {
			t.Errorf("re-link page lacks %q", want)
		}
	}
	if strings.Contains(page, "Sign it in to") {
		t.Error("a re-link offers another account")
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader("user_code=WXYZ-BCDF&key_fingerprint=3f2a+91c0+7b1e+0d44&reviewed=1&tenant=t2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, with(req))
	if !strings.Contains(rec.Body.String(), "iPhone de Ana approves with its new key") {
		t.Errorf("approved page:\n%s", rec.Body.String())
	}
	if n, tenant := f.approvals.Load(), f.tenant.Load(); n != 1 || tenant != "t2" {
		t.Errorf("approvals = %d in %v, want 1 in t2", n, tenant)
	}
}
