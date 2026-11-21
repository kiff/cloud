package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// viewQR is a computer's QR whose device key is in software (a Mac with no
// Secure Enclave): key_store software, no approval_pub and no credential.
func viewQR(r *pairRig, kind string) map[string]any {
	q := r.qr()
	delete(q, "approval_pub")
	q["platform"], q["kind"], q["name"], q["key_store"] = "macos", kind, "tafs on this Mac", "software"
	return q
}

// §5.1, §2.6: a computer whose device key is in software pairs as
// a view device, whatever its kind. Its check code is over pub alone, its
// session is view, and it never approves: not with its device key, not with
// the person's passkeys, not for another service. It isn't an approval
// device, it can't become one in place, and it can leave on its own.
func TestASoftwareKeyedComputerPairsAsAViewDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))

	// Refused shapes: another key store, the keys a view device doesn't bring, a phone.
	for name, spoil := range map[string]func(q map[string]any){
		"key_store tpm":        func(q map[string]any) { q["key_store"] = "tpm" },
		"with an approval key": func(q map[string]any) { q["approval_pub"] = b64url(r.pcApprovalDER) },
		"with a credential": func(q map[string]any) {
			q["credential"] = map[string]string{"id": b64url([]byte("cred")), "public_key": b64url(r.pcApprovalDER)}
		},
		"a phone": func(q map[string]any) {
			q["platform"], q["kind"], q["device_kind"], q["approval_pub"] = "android", "keystore", "android_phone", b64url(r.pcApprovalDER)
		},
		// A phone with no approval key at all: refused for being a phone, not for its keys.
		"a phone with no approval key": func(q map[string]any) {
			q["platform"], q["kind"], q["device_kind"] = "android", "keystore", "android_phone"
		},
		"an iPhone with no approval key": func(q map[string]any) {
			q["platform"], q["kind"], q["device_kind"] = "ios", "enclave", "iphone"
		},
	} {
		q := viewQR(r, "passkey")
		spoil(q)
		if code, out := r.approve(q); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	// Any computer kind pairs view-only with a software key; the code is over pub alone.
	for _, kind := range []string{"passkey", "enclave", "hello"} {
		rk := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
		q := viewQR(rk, kind)
		code, out := rk.approve(q)
		if pairing, _ := out["pairing"].(map[string]any); code != http.StatusCreated || pairing["code"] != auth.PairingCode(rk.pcDER, nil) {
			t.Fatalf("kind %s: %d %v", kind, code, out)
		}
		code, out = rk.claim(q["nonce"].(string), rk.pcDER, rk.pcKey)
		if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModeView {
			t.Fatalf("kind %s, the claim: %d %v", kind, code, out)
		}
	}

	q := viewQR(r, "passkey")
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("the pairing: %d %v", code, out)
	}
	code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModeView {
		t.Fatalf("the claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, err := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if err != nil || sess.ApprovalMode != auth.ApprovalModeView || len(sess.ApprovalPublicKey) != 0 {
		t.Fatalf("the session: %+v %v", sess, err)
	}
	mac, err := r.devices.Get(ctx, sess.DeviceID)
	if err != nil || mac.HasApprovalKey || mac.KeyStore != auth.KeyStoreSoftware || mac.Platform != "macos" {
		t.Fatalf("the device: %+v %v", mac, err)
	}
	// It isn't an approval device: the person still has none (the rig's phone isn't marked as one).
	if has, err := r.devices.HasApprovalDevice(ctx, "tenant-1", "user-abc", r.now.Add(-auth.ApprovalDeviceKept)); err != nil || has {
		t.Fatalf("the view device counts as one that approves: %v %v", has, err)
	}

	// The rig's phone approves, so removing it is an approval.
	if err := r.devices.MarkApprovalKey(ctx, r.phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: r.pcDER, DeviceSessionID: sess.ID, DeviceID: sess.DeviceID, ApprovalMode: auth.ApprovalModeView, KeyStore: auth.KeyStoreSoftware}
	n := 0
	signed := func(method, path, body string) string {
		n++
		return signRequest(t, r.pcKey, "tenant-1", method, path, body, r.now.Add(time.Duration(n)*time.Second))
	}
	hello := newWindowsHello(t) // the person's own passkey, registered for the account
	if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: hello.credential, Tenant: "tenant-1", PublicKey: hello.der},
		Subject: "user-abc", KeyID: auth.NewPasskeyID(), Name: "mine", CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	other := r.qr()
	otherBody, _ := json.Marshal(other)
	reg, _ := json.Marshal(map[string]string{"credential_id": b64url([]byte("another")), "public_key": b64url(r.pcApprovalDER), "name": "x"})
	for name, c := range map[string]struct{ method, path, body, header, passkey string }{
		"approving a pairing":        {http.MethodPost, "/v1/me/pairings", string(otherBody), signed(http.MethodPost, "/v1/me/pairings", string(otherBody)), ""},
		"registering a passkey":      {http.MethodPost, "/v1/me/passkeys", string(reg), signed(http.MethodPost, "/v1/me/passkeys", string(reg)), ""},
		"removing the phone":         {http.MethodDelete, "/v1/me/devices/" + r.phone.DeviceID, "", signed(http.MethodDelete, "/v1/me/devices/"+r.phone.DeviceID, ""), ""},
		"its own first approval key": {http.MethodPost, "/v1/me/device/approval-key", `{"public_key":"x"}`, signed(http.MethodPost, "/v1/me/device/approval-key", `{"public_key":"x"}`), ""},
		"a pairing with a passkey":   {http.MethodPost, "/v1/me/pairings", string(otherBody), "", hello.header(t, "tenant-1", http.MethodPost, "/v1/me/pairings", string(otherBody), r.now.Add(time.Minute), "")},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if c.header != "" {
			req.Header.Set(auth.DeviceSignatureHeader, c.header)
		}
		if c.passkey != "" {
			req.Header.Set(auth.PasskeyAssertionHeader, c.passkey)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(r.authed, pc).ServeHTTP(rec, req)
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusForbidden || got["error"] != "view_only" {
			t.Errorf("%s: %d %v, want 403 view_only", name, rec.Code, got)
		}
	}
	if phone, err := r.devices.Get(ctx, r.phone.DeviceID); err != nil || phone.IsRevoked() {
		t.Fatalf("the phone was removed: %+v %v", phone, err)
	}

	// Another service asking whether it approved one of its requests: never.
	sigMux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(auth.NewMemorySignedRequests()).Register(sigMux)
	sum := sha256.Sum256([]byte(`{}`))
	ask, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": "/v1/tafs/x/approve", "body_sha256": hex.EncodeToString(sum[:]),
		"signature": signRequest(t, r.pcKey, "tenant-1", http.MethodPost, "/v1/tafs/x/approve", `{}`, time.Now())})
	rec := httptest.NewRecorder()
	withFakeIdentity(sigMux, pc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", strings.NewReader(string(ask))))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "view_only") {
		t.Errorf("device-signature for another service: %d %s", rec.Code, rec.Body.String())
	}

	// It can leave on its own.
	if code, out := send(r.authed, &pc, http.MethodDelete, "/v1/me/devices/"+sess.DeviceID, "", signed(http.MethodDelete, "/v1/me/devices/"+sess.DeviceID, "")); code != http.StatusNoContent {
		t.Fatalf("it removes itself: %d %v", code, out)
	}
}

// The tafs browser extension pairs as platform chrome, kind extension, and
// only with key_store software: a view device. Neither value pairs any other
// way, so it can never come in as a device that approves.
func TestTheBrowserExtensionPairsOnlyAsAViewDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
	extension := func() map[string]any {
		q := viewQR(r, "extension")
		q["platform"], q["name"] = "chrome", "tafs in Chrome"
		return q
	}
	for name, spoil := range map[string]func(q map[string]any){
		"in hardware":          func(q map[string]any) { delete(q, "key_store"); q["approval_pub"] = b64url(r.pcApprovalDER) },
		"with an approval key": func(q map[string]any) { q["approval_pub"] = b64url(r.pcApprovalDER) },
		"kind hello":           func(q map[string]any) { q["kind"] = "hello" },
		"kind passkey":         func(q map[string]any) { q["kind"] = "passkey" },
		"on macos":             func(q map[string]any) { q["platform"] = "macos" },
		"as a phone":           func(q map[string]any) { q["platform"] = "android" },
		"with a device kind":   func(q map[string]any) { q["device_kind"] = "android_phone" },
		"with a credential": func(q map[string]any) {
			q["credential"] = map[string]string{"id": b64url([]byte("cred")), "public_key": b64url(r.pcApprovalDER)}
		},
	} {
		q := extension()
		spoil(q)
		if code, out := r.approve(q); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
			t.Errorf("%s: %d %v, want 400 invalid_pairing", name, code, out)
		}
	}

	q := extension()
	code, out := r.approve(q)
	if pairing, _ := out["pairing"].(map[string]any); code != http.StatusCreated || pairing["code"] != auth.PairingCode(r.pcDER, nil) {
		t.Fatalf("the pairing: %d %v", code, out)
	}
	code, out = r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModeView {
		t.Fatalf("the claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, err := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if err != nil || sess.ApprovalMode != auth.ApprovalModeView {
		t.Fatalf("the session: %+v %v", sess, err)
	}
	d, err := r.devices.Get(ctx, sess.DeviceID)
	if err != nil || d.HasApprovalKey || d.KeyStore != auth.KeyStoreSoftware || d.Platform != "chrome" {
		t.Fatalf("the device: %+v %v", d, err)
	}
}
