package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// planque finds the Chrome extension it paired among the account's devices,
// by the key the extension offered (POST /v1/devices/by-key): only with the
// account's connector key, only that account's live view-only Chrome device,
// and every other case is the same 404, so a key tells nothing else.
func TestAChromeDeviceIsFoundByItsKeyForAConnectorOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	devices := auth.NewMemoryDeviceStore()
	enrol := func(d auth.ApprovalDevice) (string, string) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		d.ID, d.PublicKey, d.CreatedAt = auth.NewDeviceID(), der, time.Now()
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d.ID, base64.RawURLEncoding.EncodeToString(der)
	}
	chrome := auth.ApprovalDevice{Tenant: "t1", Subject: "alice", Name: "tafs for Chrome", Platform: "chrome", KeyStore: auth.KeyStoreSoftware}
	chromeID, chromePub := enrol(chrome)
	_, phonePub := enrol(auth.ApprovalDevice{Tenant: "t1", Subject: "alice", Name: "phone", Platform: "ios", HasApprovalKey: true})
	approving := chrome
	approving.HasApprovalKey = true
	_, approvingPub := enrol(approving)
	hardware := chrome
	hardware.KeyStore = ""
	_, hardwarePub := enrol(hardware)
	elsewhere := chrome
	elsewhere.Tenant = "t2"
	_, elsewherePub := enrol(elsewhere)
	removedID, removedPub := enrol(chrome)
	if err := devices.Revoke(ctx, removedID, time.Now()); err != nil {
		t.Fatal(err)
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	unknownPub := base64.RawURLEncoding.EncodeToString(der)

	mux := http.NewServeMux()
	handlers.NewDeviceVerifyHandler(devices, membership.NewService(membership.NewMemoryStore())).Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	ask := func(as auth.Identity, body any) (int, string) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, as).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/devices/by-key", bytes.NewReader(b)))
		return rec.Code, string(bytes.TrimSpace(rec.Body.Bytes()))
	}

	code, body := ask(connector, map[string]string{"pub": chromePub})
	if want := `{"device_id":"` + chromeID + `"}`; code != http.StatusOK || body != want {
		t.Fatalf("the account's Chrome: %d %s, want 200 %s", code, body, want)
	}
	// A phone, a device that approves, one whose key is in hardware, another
	// account's Chrome, a removed one and a key no device holds: one answer.
	for name, pub := range map[string]string{"phone": phonePub, "approving": approvingPub, "hardware": hardwarePub,
		"another account": elsewherePub, "removed": removedPub, "unknown": unknownPub} {
		code, body := ask(connector, map[string]string{"pub": pub})
		if code != http.StatusNotFound || body != `{"error":"not_found"}` {
			t.Errorf("%s: %d %s, want the same 404 as for no device", name, code, body)
		}
	}
	// A person's session, even the account's owner, is refused: only planque asks.
	person := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodSession}
	if code, _ := ask(person, map[string]string{"pub": chromePub}); code != http.StatusForbidden {
		t.Errorf("a person: %d, want 403", code)
	}
	for _, bad := range []any{map[string]string{"pub": "not a key"}, map[string]string{}, "pub"} {
		if code, _ := ask(connector, bad); code != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", bad, code)
		}
	}
}
