package handlers_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
)

// a review: a stolen phone token must not mint a replacement
// credential that answers without the phone's signature. From a keyed
// device session, through the real route policy, approving a new device
// sign-in fails whether the new flow is keyless or carries an attacker's
// key; so does minting an API key or an approver grant. The phone still
// reaches what it needs.
func TestStolenPhoneTokenCannotMintAReplacementCredential(t *testing.T) {
	t.Parallel()
	sessions := auth.NewMemoryDeveloperSessionStore()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := now
	devices := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceClock(func() time.Time { return clock }))
	pub := http.NewServeMux()
	devices.RegisterPublic(pub)
	inner := http.NewServeMux()
	devices.RegisterAuthenticated(inner)
	handlers.NewMandatesHandler(mandate.NewMemoryStore(), nil).Register(inner)
	policed := auth.DefaultRoutePolicy().Wrap(inner)

	phoneKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	phoneDER, _ := x509.MarshalPKIXPublicKey(&phoneKey.PublicKey)
	stolen := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: phoneDER, DeviceSessionID: "sess-phone"}
	as := func(id auth.Identity, method, path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
		return rec
	}
	start := func(body any) (deviceCode, userCode string) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(b)))
		var out struct{ DeviceCode, UserCode string }
		_ = json.Unmarshal(rec.Body.Bytes(), &struct {
			DeviceCode *string `json:"device_code"`
			UserCode   *string `json:"user_code"`
		}{&out.DeviceCode, &out.UserCode})
		if rec.Code != http.StatusOK || out.UserCode == "" {
			t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
		}
		return out.DeviceCode, out.UserCode
	}
	attackerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	attackerDER, _ := x509.MarshalPKIXPublicKey(&attackerKey.PublicKey)

	for name, body := range map[string]any{
		"keyless":        map[string]string{},
		"attacker's key": map[string]string{"public_key": base64.StdEncoding.EncodeToString(attackerDER), "device_name": "Attacker"},
	} {
		deviceCode, userCode := start(body)
		if rec := as(stolen, http.MethodGet, "/v1/auth/device/pending?user_code="+userCode, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s: a phone session looked up a sign-in: %d", name, rec.Code)
		}
		if rec := as(stolen, http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": userCode}); rec.Code != http.StatusForbidden {
			t.Errorf("%s: a phone session approved a new sign-in: %d %s", name, rec.Code, rec.Body.String())
		}
		clock = clock.Add(6 * time.Second)
		b, _ := json.Marshal(map[string]string{"device_code": deviceCode})
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/token", bytes.NewReader(b)))
		if !strings.Contains(rec.Body.String(), "authorization_pending") {
			t.Errorf("%s: the new flow yielded something: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// A CLI's device session has no key, and still may not approve a
	// sign-in: that happens in the browser.
	cli := stolen
	cli.DeviceKey, cli.DeviceSessionID = nil, ""
	_, cliCode := start(map[string]string{})
	if rec := as(cli, http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": cliCode}); rec.Code != http.StatusForbidden {
		t.Errorf("a CLI session approved a sign-in: %d", rec.Code)
	}

	// Other ways to mint a credential are closed to a phone's session.
	for _, route := range [][2]string{
		{http.MethodPost, "/v1/me/approver-grants"},
		{http.MethodGet, "/v1/receipts"},
		{http.MethodPost, "/v1/me/mandates/m_1/archive"},
	} {
		if rec := as(stolen, route[0], route[1], map[string]string{}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "phone_session_forbidden") {
			t.Errorf("%s %s from a phone session: %d %s", route[0], route[1], rec.Code, rec.Body.String())
		}
	}
	// Starting a taf's setup calls need the phone's signature, which a
	// stolen token doesn't carry (option 2).
	for _, path := range []string{"/v1/keys", "/v1/me/mandates", "/v1/me/agents", "/v1/me/mandates/m_1/extra-room"} {
		if rec := as(stolen, http.MethodPost, path, map[string]string{}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
			t.Errorf("unsigned POST %s from a phone session: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// What the phone needs still reaches its handler.
	if rec := as(stolen, http.MethodGet, "/v1/me/card-exceptions", nil); rec.Code != http.StatusOK {
		t.Errorf("phone listing held calls: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(stolen, http.MethodGet, "/v1/me/devices", nil); rec.Code != http.StatusOK {
		t.Errorf("phone listing devices: %d %s", rec.Code, rec.Body.String())
	}
	// Changing the Card on a held call is an answer: it reaches its
	// handler, which wants the phone's answer signature.
	if rec := as(stolen, http.MethodPost, "/v1/me/card-exceptions/ce_1/change-card", map[string]string{}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
		t.Errorf("unsigned change-card from a phone session: %d %s", rec.Code, rec.Body.String())
	}
	// A receipt and the waiting drafts are reads the phone may make.
	for _, path := range []string{"/v1/receipts/tr_1", "/v1/me/agent-drafts"} {
		if rec := as(stolen, http.MethodGet, path, nil); strings.Contains(rec.Body.String(), "phone_session_forbidden") {
			t.Errorf("GET %s refused to a phone session: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	_ = now
}
