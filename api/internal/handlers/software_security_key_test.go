package handlers_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

// securityKeyQR is a software-keyed computer's QR (Linux, a Mac without Touch
// ID) with its security key's credential: kind passkey, key_store software.
func securityKeyQR(r *pairRig, key windowsHello) map[string]any {
	q := viewQR(r, "passkey")
	q["platform"], q["name"] = "linux", "tafs on this Linux PC"
	q["credential"] = map[string]string{"id": b64url(key.credential), "public_key": b64url(key.der)}
	return q
}

// §5.1, §2.6: a computer whose device key is in software pairs its security
// key. The check code covers it, the claim must prove it was made for this
// pairing whatever the date, and the session approves with it alone.
func TestASoftwareKeyedComputerPairsItsSecurityKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
	if !r.now.Before(handlers.PairingProofRequiredFrom) {
		t.Fatal("the rig's clock must be before PairingProofRequiredFrom, where a hardware passkey computer's proof is still optional")
	}
	key := newWindowsHello(t)
	q := securityKeyQR(r, key)

	// Refused: an approval key beside it, or a credential with another kind.
	for name, spoil := range map[string]func(q map[string]any){
		"with an approval key": func(q map[string]any) { q["approval_pub"] = b64url(r.pcApprovalDER) },
		"kind hello":           func(q map[string]any) { q["kind"] = "hello" },
		"kind enclave":         func(q map[string]any) { q["kind"] = "enclave" },
	} {
		bad := securityKeyQR(r, key)
		spoil(bad)
		if code, out := r.approve(bad); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	code, out := r.approve(q)
	pairing, _ := out["pairing"].(map[string]any)
	if code != http.StatusCreated || pairing["code"] != auth.PairingCode(r.pcDER, key.der) {
		t.Fatalf("the phone approves it, the code over pub and the security key: %d %v, want %s", code, out, auth.PairingCode(r.pcDER, key.der))
	}
	nonce := q["nonce"].(string)
	// No proof: refused, even before PairingProofRequiredFrom.
	if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["reason"] != "required" {
		t.Fatalf("a claim with no proof: %d %v", code, out)
	}
	// A proof without user verification: refused.
	right := auth.PairingCredentialMessage(nonce, b64url(r.pcDER))
	if code, out := r.claimWith(nonce, r.pcDER, r.pcKey, proofOf(key, right, nil, 0x01|0x40)); code != http.StatusBadRequest || out["reason"] != "user_verification" {
		t.Fatalf("a proof without UV: %d %v", code, out)
	}
	code, out = r.claimWith(nonce, r.pcDER, r.pcKey, proofOf(key, right, nil, made))
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModePasskey {
		t.Fatalf("the proven claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, err := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if err != nil || sess.ApprovalMode != auth.ApprovalModePasskey || len(sess.ApprovalPublicKey) != 0 {
		t.Fatalf("the session approves with the security key alone: %+v %v", sess, err)
	}
	pc, err := r.devices.Get(ctx, sess.DeviceID)
	if err != nil || pc.KeyStore != auth.KeyStoreSoftware || !pc.HasApprovalKey {
		t.Fatalf("the computer keeps its software key store and approves: %+v %v", pc, err)
	}
	stored, err := passkeys.Find(ctx, "tenant-1", key.credential)
	if err != nil || stored.DeviceID != sess.DeviceID || stored.IsRevoked() {
		t.Fatalf("the security key is registered for that computer alone: %+v %v", stored, err)
	}

	// Without a credential, kind passkey is still a view device.
	rv := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
	qv := viewQR(rv, "passkey")
	if code, out := rv.approve(qv); code != http.StatusCreated {
		t.Fatalf("a view pairing: %d %v", code, out)
	}
	if code, out := rv.claim(qv["nonce"].(string), rv.pcDER, rv.pcKey); code != http.StatusOK || out["approval_mode"] != auth.ApprovalModeView {
		t.Fatalf("a software key with no security key: %d %v", code, out)
	}
}

// What a software-keyed computer's session may approve with: its security
// key's fresh assertion, user verified, one request at a time. Its device key
// grants nothing, and no account passkey, window or two-request assertion
// counts. The same account passkey and two-request assertion do count for a
// computer whose key is in hardware, so each refusal is the software rule's.
func TestASoftwareKeysSecurityKeyApprovesAloneAndOneAtATime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := http.NewServeMux()
	inner.HandleFunc("POST /v1/me/mandates", func(w http.ResponseWriter, r *http.Request) {
		if with, ok := auth.SignedWithFrom(r.Context()); !ok || !with.Critical() || with.Passkey == nil {
			t.Errorf("reached without a passkey's approval: %+v", with)
		}
		w.WriteHeader(http.StatusOK)
	})
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	passkeys := auth.NewMemoryPasskeyStore()
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(inner)

	deviceKey, deviceDER := p256(t)
	linux := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-linux", DeviceID: "dev_linux", ApprovalMode: auth.ApprovalModePasskey, KeyStore: auth.KeyStoreSoftware}
	windows := linux
	windows.KeyStore, windows.DeviceSessionID, windows.DeviceID = "", "sess-win", "dev_win"
	if linux.ViewOnly() || !linux.SoftwareKeyPasskey() || linux.ShownApprovalMode() != auth.ApprovalModePasskey || windows.SoftwareKeyPasskey() {
		t.Fatalf("the sessions: linux view=%v software=%v shown=%q; windows software=%v",
			linux.ViewOnly(), linux.SoftwareKeyPasskey(), linux.ShownApprovalMode(), windows.SoftwareKeyPasskey())
	}
	for _, mode := range []string{auth.ApprovalModeView, auth.ApprovalModeKey, ""} {
		if id := (auth.Identity{ApprovalMode: mode, KeyStore: auth.KeyStoreSoftware}); !id.ViewOnly() || id.SoftwareKeyPasskey() {
			t.Errorf("a software key in mode %q approves", mode)
		}
	}

	register := func(k windowsHello, deviceID string) {
		if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: k.credential, Tenant: "tenant-1", PublicKey: k.der},
			Subject: "user-abc", DeviceID: deviceID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	securityKey, account := newWindowsHello(t), newWindowsHello(t)
	register(securityKey, "dev_linux")
	register(account, "")

	n := 0
	at := func() time.Time { n++; return now.Add(time.Duration(n) * time.Second) }
	call := func(id auth.Identity, passkey, signature string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/mandates", strings.NewReader(`{}`))
		if passkey != "" {
			req.Header.Set(auth.PasskeyAssertionHeader, passkey)
		}
		if signature != "" {
			req.Header.Set(auth.DeviceSignatureHeader, signature)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		e, _ := out["error"].(string)
		return rec.Code, e
	}
	assertion := func(k windowsHello, with string) string {
		return k.header(t, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, at(), with)
	}
	other := strings.Repeat("ab", 32)

	// Approved: its own security key, user verified, this one request.
	if code, e := call(linux, assertion(securityKey, ""), ""); code != http.StatusOK {
		t.Fatalf("its security key: %d %s", code, e)
	}
	// The hardware computer may use what the software one may not.
	if code, e := call(windows, assertion(account, ""), ""); code != http.StatusOK {
		t.Fatalf("an account passkey, for a hardware computer: %d %s", code, e)
	}

	unverified := securityKey
	unverified.flags = 0x01
	for _, c := range []struct {
		name, passkey, signature string
		status                   int
		want                     string
	}{
		{"its device key alone", "", signRequest(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, at()), 403, "passkey_required"},
		{"nothing", "", "", 403, "passkey_required"},
		{"its security key without UV", assertion(unverified, ""), "", 403, "user_verification_required"},
		{"an account passkey", assertion(account, ""), "", 403, "passkey_not_this_device"},
		{"its security key over two requests", assertion(securityKey, other), "", 403, "passkey_invalid"},
		{"its device key naming a window", "", signRequestWindow(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, at(), "win_"+strings.Repeat("a", 26)), 403, "security_key_each_request"},
		{"its device key over two requests", "", "t=" + at().Format(time.RFC3339) + ",v3=AAAA,with=sha256:" + other, 403, "security_key_each_request"},
	} {
		if code, e := call(linux, c.passkey, c.signature); code != c.status || e != c.want {
			t.Errorf("%s: %d %s, want %d %s", c.name, code, e, c.status, c.want)
		}
	}
}

// A software-keyed computer's security key opens no approval window, even with
// a good assertion: each request is approved alone. The same assertion opens
// one for a computer whose key is in hardware.
func TestASoftwareKeysSecurityKeyOpensNoWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	windows := auth.NewMemoryWindowStore()
	inner := http.NewServeMux()
	passkeys := auth.NewMemoryPasskeyStore()
	wh := handlers.NewApprovalWindowHandler(windows, nil).WithPasskeys(passkeys)
	handlers.SetClock(wh, func() time.Time { return now })
	wh.Register(inner)
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithWindows(windows).WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(inner)

	_, deviceDER := p256(t)
	n := 0
	try := func(keyStore string) (int, map[string]any) {
		n++
		id := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
			DeviceKey: deviceDER, DeviceSessionID: "sess-" + keyStore, DeviceID: "dev-" + keyStore, ApprovalMode: auth.ApprovalModePasskey, KeyStore: keyStore}
		key := newWindowsHello(t)
		if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: key.credential, Tenant: "tenant-1", PublicKey: key.der},
			Subject: "user-abc", DeviceID: id.DeviceID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		nonce := make([]byte, 16)
		_, _ = rand.Read(nonce)
		b, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: id.DeviceSessionID, Device: id.DeviceID,
			Expires: now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: hex.EncodeToString(nonce)})
		req := httptest.NewRequest(http.MethodPost, "/v1/me/window", strings.NewReader(string(b)))
		req.Header.Set(auth.PasskeyAssertionHeader, key.header(t, "tenant-1", http.MethodPost, "/v1/me/window", string(b), now.Add(time.Duration(n)*time.Second), ""))
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := try(auth.KeyStoreSoftware); code != http.StatusForbidden || out["error"] != "security_key_each_request" {
		t.Fatalf("a software key's security key opened a window: %d %v", code, out)
	}
	if _, err := windows.OpenFor(ctx, "dev-"+auth.KeyStoreSoftware, now); err == nil {
		t.Fatal("a window is open for the software-keyed computer")
	}
	if code, out := try(""); code != http.StatusCreated {
		t.Fatalf("a hardware computer's own passkey: %d %v", code, out)
	}
}

// A software-keyed computer that paired its security key never gains an
// approval key, which would be in software like its device key: it can't
// register one, or re-link one, signed by its device key.
func TestASoftwareKeysSessionGainsNoApprovalKey(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	linux := r.phone
	linux.ApprovalKey, linux.ApprovalMode, linux.KeyStore = nil, auth.ApprovalModePasskey, auth.KeyStoreSoftware
	_, newDER := p256(t)
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(newDER)})
	const register = "/v1/me/device/approval-key"
	if code, out := send(r.authed, &linux, http.MethodPost, register, string(body), signRequest(t, r.device, "tenant-1", http.MethodPost, register, string(body), r.now)); code != http.StatusForbidden || out["error"] != "security_key_only" {
		t.Fatalf("registering an approval key: %d %v", code, out)
	}
	const relink = "/v1/me/device/relink"
	if code, out := send(r.authed, &linux, http.MethodPost, relink, `{}`, signRequest(t, r.device, "tenant-1", http.MethodPost, relink, `{}`, r.now.Add(time.Second))); code != http.StatusForbidden || out["error"] != "security_key_only" {
		t.Fatalf("re-linking an approval key: %d %v", code, out)
	}
}
