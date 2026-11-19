package handlers_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
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

// windowsHello is a computer's own platform passkey as Windows Hello makes
// its assertions (webauthn.dll): ES256 in the TPM, user verified, never
// synced. Each field can be changed to test one of KIFF's checks.
type windowsHello struct {
	key        *ecdsa.PrivateKey
	der        []byte
	credential []byte
	rpID       string
	origin     string
	flags      byte
	count      uint32
}

func newWindowsHello(t *testing.T) windowsHello {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	cred := make([]byte, 32)
	_, _ = rand.Read(cred)
	// UP and UV; a TPM credential keeps no counter.
	return windowsHello{key: k, der: der, credential: cred, rpID: "kiff.dev", origin: "https://kiff.dev", flags: 0x01 | 0x04}
}

// assert approves message: the challenge is base64url(sha256(message)).
func (w windowsHello) assert(t *testing.T, message []byte) auth.Assertion {
	t.Helper()
	digest := sha256.Sum256(message)
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": base64.RawURLEncoding.EncodeToString(digest[:]), "origin": w.origin, "crossOrigin": false})
	rp := sha256.Sum256([]byte(w.rpID))
	authData := binary.BigEndian.AppendUint32(append(append([]byte(nil), rp[:]...), w.flags), w.count)
	ch := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte(nil), authData...), ch[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, w.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	e := base64.RawURLEncoding.EncodeToString
	return auth.Assertion{CredentialID: e(w.credential), ClientDataJSON: e(client), AuthenticatorData: e(authData), Signature: e(sig)}
}

// header is the KIFF-Passkey-Assertion for a request, signed at at; with
// names another request approved together with it (sha256 hex), or "".
func (w windowsHello) header(t *testing.T, tenant, method, path, body string, at time.Time, with string) string {
	t.Helper()
	signedAt := at.UTC().Format(time.RFC3339)
	message := auth.RequestMessage(tenant, method, path, []byte(body), signedAt)
	if with != "" {
		own := sha256.Sum256(message)
		message = auth.RequestsMessage(tenant, signedAt, hex.EncodeToString(own[:]), with)
	}
	a, _ := json.Marshal(w.assert(t, message))
	h := "t=" + signedAt + ",a=" + base64.RawURLEncoding.EncodeToString(a)
	if with != "" {
		h += ",with=sha256:" + with
	}
	return h
}

// passkeyQR is a Windows computer's QR: kind passkey, no approval_pub, and
// its own credential.
func passkeyQR(r *pairRig, hello windowsHello) map[string]any {
	q := r.qr()
	delete(q, "approval_pub")
	q["platform"], q["kind"], q["name"] = "windows", "passkey", "tafs on this Windows PC"
	q["credential"] = map[string]string{"id": b64url(hello.credential), "public_key": b64url(hello.der)}
	return q
}

// §5.1, §5.2, §2.6: a Windows computer pairs with its own platform passkey.
// The phone's one approval registers it for that device, the check code
// covers it, and the claimed session approves only with it.
func TestAWindowsComputerPairsWithItsOwnPasskey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
	hello := newWindowsHello(t)
	q := passkeyQR(r, hello)
	code, out := r.approve(q)
	pairing, _ := out["pairing"].(map[string]any)
	if code != http.StatusCreated || pairing["code"] != auth.PairingCode(r.pcDER, hello.der) {
		t.Fatalf("the phone approves a passkey computer: %d %v, want code %s", code, out, auth.PairingCode(r.pcDER, hello.der))
	}
	code, out = r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModePasskey {
		t.Fatalf("the claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, err := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if err != nil || sess.ApprovalMode != auth.ApprovalModePasskey || len(sess.ApprovalPublicKey) != 0 || sess.DeviceID != out["device_id"] {
		t.Fatalf("the session approves with the passkey alone: %+v %v", sess, err)
	}
	if pc, err := r.devices.Get(ctx, sess.DeviceID); err != nil || !pc.HasApprovalKey || pc.Platform != "windows" {
		t.Fatalf("the computer counts as a device that approves: %+v %v", pc, err)
	}
	key, err := passkeys.Find(ctx, "tenant-1", hello.credential)
	if err != nil || key.DeviceID != sess.DeviceID || key.Subject != "user-abc" || string(key.PublicKey) != string(hello.der) || key.IsRevoked() {
		t.Fatalf("the passkey is registered for that device: %+v %v", key, err)
	}

	// The same credential again, on another computer: already registered.
	other := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
	if code, out := other.approve(passkeyQR(other, hello)); code != http.StatusConflict || out["error"] != "key_registered" {
		t.Fatalf("a credential already registered: %d %v", code, out)
	}

	// A computer's own passkey has no id of its own: the list shows it by its
	// device, and DELETE /v1/me/passkeys/{id}, even signed with the approval
	// key, can't name it, neither by its credential id nor by an empty id. It
	// goes only with its computer.
	_, list := send(r.authed, &r.phone, http.MethodGet, "/v1/me/passkeys", "", "")
	if keys, _ := list["passkeys"].([]any); len(keys) != 1 || keys[0].(map[string]any)["id"] != "" || keys[0].(map[string]any)["device_id"] != sess.DeviceID {
		t.Fatalf("the person's passkeys: %v", list)
	}
	for i, del := range []string{"/v1/me/passkeys/" + b64url(hello.credential), "/v1/me/passkeys/", "/v1/me/passkeys/%20"} {
		code, out := send(r.authed, &r.phone, http.MethodDelete, del, "", signRequest(t, r.approval, "tenant-1", http.MethodDelete, del, "", r.now.Add(time.Duration(i+1)*time.Second)))
		if code == http.StatusNoContent || code/100 == 2 {
			t.Fatalf("DELETE %s removed something: %d %v", del, code, out)
		}
	}
	if key, err := passkeys.Find(ctx, "tenant-1", hello.credential); err != nil || key.IsRevoked() {
		t.Fatalf("the computer's own passkey was removed by its own route: %+v %v", key, err)
	}

	// Removing the computer removes its passkey.
	if code, out := send(r.authed, &r.phone, http.MethodDelete, "/v1/me/devices/"+sess.DeviceID, "", ""); code != http.StatusNoContent {
		t.Fatalf("the phone removes the computer: %d %v", code, out)
	}
	if key, err := passkeys.Find(ctx, "tenant-1", hello.credential); err != nil || !key.IsRevoked() {
		t.Fatalf("the computer's passkey outlived it: %+v %v", key, err)
	}
}

// §5.5's field checks, for kind passkey.
func TestPasskeyPairingFieldsAreChecked(t *testing.T) {
	t.Parallel()
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	for _, tc := range []struct {
		name   string
		change func(r *pairRig, q map[string]any)
	}{
		{"with approval_pub", func(r *pairRig, q map[string]any) { q["approval_pub"] = b64url(r.pcApprovalDER) }},
		{"a credential with kind hello", func(r *pairRig, q map[string]any) { q["kind"] = "hello"; q["approval_pub"] = b64url(r.pcApprovalDER) }},
		{"credential key not P-256", func(r *pairRig, q map[string]any) {
			q["credential"].(map[string]string)["public_key"] = b64url(p384DER)
		}},
		{"credential key is pub", func(r *pairRig, q map[string]any) {
			q["credential"].(map[string]string)["public_key"] = b64url(r.pcDER)
		}},
		{"credential id empty", func(r *pairRig, q map[string]any) { q["credential"].(map[string]string)["id"] = "" }},
		{"credential id of 1024 bytes", func(r *pairRig, q map[string]any) {
			q["credential"].(map[string]string)["id"] = b64url(make([]byte, 1024))
		}},
		{"credential id padded", func(r *pairRig, q map[string]any) {
			q["credential"].(map[string]string)["id"] = base64.URLEncoding.EncodeToString(make([]byte, 31))
		}},
		{"key_store", func(r *pairRig, q map[string]any) { q["key_store"] = "software" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
			q := passkeyQR(r, newWindowsHello(t))
			tc.change(r, q)
			if code, out := r.approve(q); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
				t.Fatalf("got %d %v, want 400 invalid_pairing", code, out)
			}
		})
	}
}

// §2.6, the six checks for a computer's own passkey, each with its
// negative, on every route that grants authority: (1) user verification,
// (2) the exact rpIdHash, (3) the agreed origin, which binds nothing, (4)
// only the device it was registered with, and not once that device is
// removed, (5) never synced, (6) each challenge used once, the counter
// not relied on. A window and the device key approve nothing for it.
func TestAComputersOwnPasskeyApprovesExactlyWhatItSigned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reached := 0
	inner := http.NewServeMux()
	reach := func(w http.ResponseWriter, r *http.Request) {
		if with, ok := auth.SignedWithFrom(r.Context()); !ok || !with.Critical() || with.Passkey == nil || with.Assertion == "" {
			t.Errorf("%s reached without the passkey's approval: %+v", r.URL.Path, with)
		}
		reached++
		w.WriteHeader(http.StatusOK)
	}
	inner.HandleFunc("POST /v1/me/mandates", reach)
	inner.HandleFunc("POST /v1/me/agent-drafts/{id}/confirm", reach)
	inner.HandleFunc("POST /v1/me/alert-targets", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	passkeys := auth.NewMemoryPasskeyStore()
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(inner)

	deviceKey, deviceDER := p256(t)
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-pc", DeviceID: "dev_pc", ApprovalMode: auth.ApprovalModePasskey}
	register := func(h windowsHello, deviceID, subject string) {
		if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: h.credential, Tenant: "tenant-1", PublicKey: h.der},
			Subject: subject, DeviceID: deviceID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	hello := newWindowsHello(t)
	register(hello, "dev_pc", "user-abc")

	n := 0
	call := func(id auth.Identity, method, path, body, passkey, signature string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
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
	// Each approval is of a fresh request (another second), so none is a replay.
	fresh := func(h windowsHello, path, body string) string {
		n++
		return h.header(t, "tenant-1", http.MethodPost, path, body, now.Add(time.Duration(n)*time.Second), "")
	}

	// Approved: both routes, twice each, with a counter that stays at 0.
	for _, path := range []string{"/v1/me/mandates", "/v1/me/agent-drafts/dr_1/confirm"} {
		for range 2 {
			if code, e := call(pc, http.MethodPost, path, `{"a":1}`, fresh(hello, path, `{"a":1}`), ""); code != http.StatusOK {
				t.Fatalf("%s approved by the computer's passkey: %d %s", path, code, e)
			}
		}
	}
	// (6) The same approval twice: the second is refused.
	once := fresh(hello, "/v1/me/mandates", `{"a":1}`)
	if code, _ := call(pc, http.MethodPost, "/v1/me/mandates", `{"a":1}`, once, ""); code != http.StatusOK {
		t.Fatal("the first use")
	}
	if code, e := call(pc, http.MethodPost, "/v1/me/mandates", `{"a":1}`, once, ""); code != http.StatusConflict || e != "signature_replayed" {
		t.Errorf("(6) the same challenge twice: %d %s", code, e)
	}
	before := reached

	changed := func(change func(*windowsHello)) windowsHello { h := hello; change(&h); return h }
	stranger := newWindowsHello(t)
	otherDevice := newWindowsHello(t)
	register(otherDevice, "dev_other", "user-abc")
	otherPerson := newWindowsHello(t)
	register(otherPerson, "dev_pc", "user-zed")
	removed := newWindowsHello(t)
	register(removed, "dev_pc", "user-abc")
	for _, c := range []struct {
		name   string
		header func() string
		status int
		want   string
	}{
		{"(1) user present, not verified", func() string {
			return fresh(changed(func(h *windowsHello) { h.flags = 0x01 }), "/v1/me/mandates", `{}`)
		}, 403, "user_verification_required"},
		{"(2) another RP ID", func() string {
			return fresh(changed(func(h *windowsHello) { h.rpID = "kiff.dev.example.com" }), "/v1/me/mandates", `{}`)
		}, 403, "passkey_invalid"},
		{"(3) not the agreed origin", func() string {
			return fresh(changed(func(h *windowsHello) { h.origin = "https://example.com" }), "/v1/me/mandates", `{}`)
		}, 403, "passkey_invalid"},
		{"(4) another device's passkey", func() string { return fresh(otherDevice, "/v1/me/mandates", `{}`) }, 403, "passkey_not_this_device"},
		{"(4) another person's passkey", func() string { return fresh(otherPerson, "/v1/me/mandates", `{}`) }, 403, "passkey_invalid"},
		{"(5) backup eligible", func() string {
			return fresh(changed(func(h *windowsHello) { h.flags |= 0x08 }), "/v1/me/mandates", `{}`)
		}, 403, "passkey_synced"},
		{"(5) backed up", func() string {
			return fresh(changed(func(h *windowsHello) { h.flags |= 0x08 | 0x10 }), "/v1/me/mandates", `{}`)
		}, 403, "passkey_synced"},
		{"a passkey KIFF never registered", func() string { return fresh(stranger, "/v1/me/mandates", `{}`) }, 403, "passkey_invalid"},
		{"signed by another key", func() string {
			return fresh(changed(func(h *windowsHello) { h.key = stranger.key }), "/v1/me/mandates", `{}`)
		}, 403, "passkey_invalid"},
		{"another request's challenge", func() string { return fresh(hello, "/v1/me/mandates", `{"a":2}`) }, 403, "passkey_invalid"},
		{"signed 6 minutes ago", func() string {
			return hello.header(t, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, now.Add(-6*time.Minute), "")
		}, 400, "signed_at_out_of_range"},
		{"a malformed header", func() string { return "t=" + now.Format(time.RFC3339) + ",a=!" }, 403, "passkey_invalid"},
		{"no assertion at all", func() string { return "" }, 403, "passkey_required"},
	} {
		body := `{}`
		if c.name == "another request's challenge" {
			body = `{"a":1}`
		}
		if code, e := call(pc, http.MethodPost, "/v1/me/mandates", body, c.header(), ""); code != c.status || e != c.want {
			t.Errorf("%s: %d %s, want %d %s", c.name, code, e, c.status, c.want)
		}
	}
	// (4) Once the computer's passkeys are removed with it, its own is refused.
	if err := passkeys.RevokeForDevice(ctx, "dev_pc", now); err != nil {
		t.Fatal(err)
	}
	if code, e := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, fresh(removed, "/v1/me/mandates", `{}`), ""); code != http.StatusForbidden || e != "passkey_invalid" {
		t.Errorf("(4) the removed computer's passkey: %d %s", code, e)
	}
	register(hello2(t, &hello), "dev_pc", "user-abc")

	// The device key, or a window's tap, approves nothing for it.
	n++
	if code, e := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, "", signRequest(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, now.Add(time.Duration(n)*time.Second))); code != http.StatusForbidden || e != "passkey_required" {
		t.Errorf("the device key's signature: %d %s", code, e)
	}
	n++
	if code, e := call(pc, http.MethodPost, "/v1/me/agent-drafts/dr_1/confirm", `{}`, "", signRequestWindow(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/agent-drafts/dr_1/confirm", `{}`, now.Add(time.Duration(n)*time.Second), auth.NewWindowID())); code != http.StatusForbidden || e != "passkey_required" {
		t.Errorf("a window's tap: %d %s", code, e)
	}
	if reached != before {
		t.Fatalf("a refused approval reached its handler (%d)", reached-before)
	}

	// Approving two requests at once (with=): this one's use is its own, once.
	other := sha256.Sum256([]byte("the other service's kiff.request.v1"))
	n++
	together := hello.header(t, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, now.Add(time.Duration(n)*time.Second), hex.EncodeToString(other[:]))
	if code, e := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, together, ""); code != http.StatusOK {
		t.Errorf("two requests approved at once: %d %s", code, e)
	}
	if code, e := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, together, ""); code != http.StatusConflict {
		t.Errorf("the same two-request approval again: %d %s", code, e)
	}

	// A signed call that grants nothing is still its device key's.
	n++
	if code, e := call(pc, http.MethodPost, "/v1/me/alert-targets", `{}`, "", signRequest(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/alert-targets", `{}`, now.Add(time.Duration(n)*time.Second))); code != http.StatusOK {
		t.Errorf("a signed call that grants nothing: %d %s", code, e)
	}
	// A phone's session ignores a passkey header: its approval key signs.
	phone := pc
	phone.ApprovalMode, phone.DeviceID = auth.ApprovalModeKey, "dev_pc"
	if code, e := call(phone, http.MethodPost, "/v1/me/mandates", `{}`, fresh(hello, "/v1/me/mandates", `{}`), ""); code != http.StatusForbidden || e != "approval_key_required" {
		t.Errorf("a passkey header from a session held to its approval key: %d %s", code, e)
	}
}

// hello2 is the rig's passkey under a new credential id, so it can be
// registered again after its first was removed.
func hello2(t *testing.T, h *windowsHello) windowsHello {
	t.Helper()
	cred := make([]byte, 32)
	_, _ = rand.Read(cred)
	h.credential = cred
	return *h
}

// A Windows computer's half of a two-request approval reaches another
// service (tafs cloud), which asks KIFF whether the computer approved it:
// POST /v1/me/device-signature takes the passkey's assertion in place of a
// signature, for a session that approves with it, and answers key critical,
// once. The six checks hold there as on KIFF's own routes.
func TestAnotherServiceAsksWhetherTheComputersPasskeyApprovedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()
	passkeys := auth.NewMemoryPasskeyStore()
	mux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(nil).WithPasskeys(passkeys, auth.KiffRelyingParty).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	_, deviceDER := p256(t)
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-pc", DeviceID: "dev_pc", ApprovalMode: auth.ApprovalModePasskey}
	hello := newWindowsHello(t)
	if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: hello.credential, Tenant: "tenant-1", PublicKey: hello.der},
		Subject: "user-abc", DeviceID: "dev_pc", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	const uri = "/v1/grants/g_1/approve"
	body := `{"grant":"g_1"}`
	sum := sha256.Sum256([]byte(body))
	ask := func(id auth.Identity, fields map[string]string) (int, map[string]any) {
		req := map[string]string{"method": http.MethodPost, "uri": uri, "body_sha256": hex.EncodeToString(sum[:])}
		for k, v := range fields {
			req[k] = v
		}
		b, _ := json.Marshal(req)
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", strings.NewReader(string(b))))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	other := sha256.Sum256([]byte("KIFF's own request in the same approval"))
	n := 0
	header := func(h windowsHello, with string) string {
		n++
		return h.header(t, "tenant-1", http.MethodPost, uri, body, now.Add(time.Duration(n)*time.Second), with)
	}

	together := header(hello, hex.EncodeToString(other[:]))
	if code, out := ask(pc, map[string]string{"passkey": together}); code != http.StatusOK || out["key"] != auth.KeyCritical || out["grant"] == nil || out["window"] != nil {
		t.Fatalf("the computer's half of a two-request approval: %d %v", code, out)
	}
	if code, out := ask(pc, map[string]string{"passkey": together}); code != http.StatusConflict {
		t.Fatalf("the same approval again: %d %v", code, out)
	}
	if code, out := ask(pc, map[string]string{"passkey": header(hello, "")}); code != http.StatusOK || out["key"] != auth.KeyCritical {
		t.Fatalf("one request: %d %v", code, out)
	}
	synced := hello
	synced.flags |= 0x08
	unverified := hello
	unverified.flags = 0x01
	elsewhere := newWindowsHello(t)
	if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: elsewhere.credential, Tenant: "tenant-1", PublicKey: elsewhere.der},
		Subject: "user-abc", DeviceID: "dev_other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]windowsHello{"backup eligible": synced, "not verified": unverified, "another device's": elsewhere} {
		if code, out := ask(pc, map[string]string{"passkey": header(h, "")}); code != http.StatusForbidden || out["error"] != "signature_invalid" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	keyMode := pc
	keyMode.ApprovalMode = auth.ApprovalModeKey
	if code, out := ask(keyMode, map[string]string{"passkey": header(hello, "")}); code != http.StatusForbidden || out["error"] != "passkey_not_this_session" {
		t.Fatalf("a passkey from a session held to an approval key: %d %v", code, out)
	}
	if code, _ := ask(pc, map[string]string{"passkey": header(hello, ""), "signature": "t=x,v1=y"}); code != http.StatusBadRequest {
		t.Fatalf("both a signature and a passkey: %d", code)
	}
	if code, _ := ask(pc, nil); code != http.StatusBadRequest {
		t.Fatalf("neither: %d", code)
	}
}

// §2.6, B2: the person registers a passkey for the account (a phone's
// passkey through a QR code, a security key), signed by their phone's
// approval key; a computer with no approval key of its own (Linux, a Mac
// without Touch ID) pairs as kind passkey with no credential, and approves
// with it, an assertion each time. A synced passkey approves too, and says
// so. Registering takes the approval key and an admin; removing is an
// approval; another person's passkey approves nothing.
func TestAPersonsPasskeyApprovesForAComputerWithoutAnApprovalKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
	reached := 0
	r.inner.HandleFunc("POST /v1/me/mandates", func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusOK) })
	r.authed = auth.DefaultRoutePolicy().WithClock(func() time.Time { return r.now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(r.inner)
	phone := r.phone
	phone.Roles = []string{"tenant_owner"}
	n := 0
	signedBy := func(key *ecdsa.PrivateKey, method, path, body string) string {
		n++
		return signRequest(t, key, "tenant-1", method, path, body, r.now.Add(time.Duration(n)*time.Second))
	}
	hello := newWindowsHello(t) // here, the person's phone passkey through a QR code
	hello.flags |= 0x08 | 0x10  // synced to their cloud account
	reg, _ := json.Marshal(map[string]string{"credential_id": b64url(hello.credential), "public_key": base64.StdEncoding.EncodeToString(hello.der), "name": "My phone's passkey"})
	const passkeysPath = "/v1/me/passkeys"

	// Registering: only the approval key, and only an admin.
	if code, out := send(r.authed, &phone, http.MethodPost, passkeysPath, string(reg), signedBy(r.device, http.MethodPost, passkeysPath, string(reg))); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("registering with the device key: %d %v", code, out)
	}
	member := phone
	member.Roles, member.MemberRole = nil, "approver"
	if code, out := send(r.authed, &member, http.MethodPost, passkeysPath, string(reg), signedBy(r.approval, http.MethodPost, passkeysPath, string(reg))); code != http.StatusForbidden || out["error"] != "admins_only" {
		t.Fatalf("registering as a member who isn't an admin: %d %v", code, out)
	}
	code, out := send(r.authed, &phone, http.MethodPost, passkeysPath, string(reg), signedBy(r.approval, http.MethodPost, passkeysPath, string(reg)))
	pk, _ := out["passkey"].(map[string]any)
	if code != http.StatusCreated || pk["id"] == nil || pk["name"] != "My phone's passkey" || pk["synced"] != nil {
		t.Fatalf("registering with the approval key: %d %v", code, out)
	}
	keyID := pk["id"].(string)
	if code, out := send(r.authed, &phone, http.MethodPost, passkeysPath, string(reg), signedBy(r.approval, http.MethodPost, passkeysPath, string(reg))); code != http.StatusConflict || out["error"] != "passkey_registered" {
		t.Fatalf("the same passkey again: %d %v", code, out)
	}

	// A Linux computer pairs as kind passkey with no credential: the code is over pub alone.
	q := r.qr()
	delete(q, "approval_pub")
	q["kind"] = "passkey"
	code, out = r.approve(q)
	if pairing, _ := out["pairing"].(map[string]any); code != http.StatusCreated || pairing["code"] != auth.PairingCode(r.pcDER, nil) {
		t.Fatalf("pairing a passkey computer with no credential: %d %v", code, out)
	}
	code, out = r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModePasskey {
		t.Fatalf("the claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, _ := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if sess.ApprovalMode != auth.ApprovalModePasskey || len(sess.ApprovalPublicKey) != 0 {
		t.Fatalf("the computer's session: %+v", sess)
	}
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: r.pcDER, DeviceSessionID: sess.ID, DeviceID: sess.DeviceID, ApprovalMode: auth.ApprovalModePasskey}
	assertWith := func(h windowsHello, method, path, body string) string {
		n++
		return h.header(t, "tenant-1", method, path, body, r.now.Add(time.Duration(n)*time.Second), "")
	}
	call := func(id auth.Identity, method, path, body, passkey string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(auth.PasskeyAssertionHeader, passkey)
		rec := httptest.NewRecorder()
		withFakeIdentity(r.authed, id).ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The computer approves with the person's passkey, synced or not, and the list says synced.
	if code, out := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, assertWith(hello, http.MethodPost, "/v1/me/mandates", `{}`)); code != http.StatusOK || reached != 1 {
		t.Fatalf("approved with the person's passkey: %d %v", code, out)
	}
	_, list := send(r.authed, &phone, http.MethodGet, passkeysPath, "", "")
	keys, _ := list["passkeys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["synced"] != true || keys[0].(map[string]any)["id"] != keyID {
		t.Fatalf("the person's passkeys: %v", list)
	}
	// Another person's passkey approves nothing.
	stranger := newWindowsHello(t)
	if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: stranger.credential, Tenant: "tenant-1", PublicKey: stranger.der},
		Subject: "user-zed", KeyID: auth.NewPasskeyID(), Name: "theirs", CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	if code, out := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, assertWith(stranger, http.MethodPost, "/v1/me/mandates", `{}`)); code != http.StatusForbidden || out["error"] != "passkey_invalid" {
		t.Fatalf("another person's passkey: %d %v", code, out)
	}

	// Removing it is an approval: never the phone's device key alone; the computer's assertion does.
	del := passkeysPath + "/" + keyID
	if code, out := send(r.authed, &phone, http.MethodDelete, del, "", signedBy(r.device, http.MethodDelete, del, "")); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("removing with the device key: %d %v", code, out)
	}
	if code, out := call(pc, http.MethodDelete, del, "", assertWith(hello, http.MethodDelete, del, "")); code != http.StatusNoContent {
		t.Fatalf("the computer removes it with its assertion: %d %v", code, out)
	}
	if code, out := call(pc, http.MethodPost, "/v1/me/mandates", `{}`, assertWith(hello, http.MethodPost, "/v1/me/mandates", `{}`)); code != http.StatusForbidden || reached != 1 {
		t.Fatalf("the removed passkey still approves: %d %v", code, out)
	}
	if code, _ := send(r.authed, &phone, http.MethodDelete, del, "", signedBy(r.approval, http.MethodDelete, del, "")); code != http.StatusNotFound {
		t.Fatalf("removing it again: %d", code)
	}
}
