package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// pairRig is a phone's session with both keys, behind the route policy,
// and a computer with its own two keys (the desktop-client design, §5).
type pairRig struct {
	t        *testing.T
	now      time.Time
	sessions *auth.MemoryDeveloperSessionStore
	devices  *auth.MemoryDeviceStore
	windows  *auth.MemoryWindowStore
	authed   http.Handler // the policy, then the device routes
	inner    *http.ServeMux
	pub      *http.ServeMux
	phone    auth.Identity
	device   *ecdsa.PrivateKey // the phone's
	approval *ecdsa.PrivateKey
	// The computer's keys.
	pcKey                *ecdsa.PrivateKey
	pcDER, pcApprovalDER []byte
}

func newPairRig(t *testing.T, extra ...handlers.DeviceAuthOption) *pairRig {
	t.Helper()
	return newPairRigWithSessions(t, nil, extra...)
}

// newPairRigWithSessions is newPairRig whose handler keeps its sessions in
// wrap(the rig's store), as a guarded store does; nil keeps them as they are.
func newPairRigWithSessions(t *testing.T, wrap func(auth.DeveloperSessionStore) auth.DeveloperSessionStore, extra ...handlers.DeviceAuthOption) *pairRig {
	t.Helper()
	r := &pairRig{t: t, now: time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC), sessions: auth.NewMemoryDeveloperSessionStore(),
		devices: auth.NewMemoryDeviceStore(), windows: auth.NewMemoryWindowStore()}
	opts := append([]handlers.DeviceAuthOption{handlers.WithDeviceStore(r.devices), handlers.WithDevicePairings(auth.NewMemoryPairingStore()),
		handlers.WithDeviceClock(func() time.Time { return r.now })}, extra...)
	var sessions auth.DeveloperSessionStore = r.sessions
	if wrap != nil {
		sessions = wrap(sessions)
	}
	h := handlers.NewDeviceAuthHandler(sessions, opts...)
	r.pub = http.NewServeMux()
	h.RegisterPublic(r.pub)
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	r.inner = inner
	r.authed = auth.DefaultRoutePolicy().WithWindows(r.windows).WithClock(func() time.Time { return r.now }).Wrap(inner)
	var deviceDER, approvalDER []byte
	r.device, deviceDER = p256(t)
	r.approval, approvalDER = p256(t)
	r.phone = auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone", ApprovalKey: approvalDER}
	// The phone that approves is one of the person's devices, as a signed-in phone is.
	if err := r.devices.Insert(context.Background(), auth.ApprovalDevice{ID: r.phone.DeviceID, Tenant: r.phone.Tenant, Subject: r.phone.Subject,
		Name: "phone", Platform: "android", Kind: auth.DeviceOther, PublicKey: deviceDER, CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	r.pcKey, r.pcDER = p256(t)
	_, r.pcApprovalDER = p256(t)
	return r
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// qr is the computer's QR for the rig's computer keys, with a fresh nonce.
func (r *pairRig) qr() map[string]any {
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	return map[string]any{"v": "kiff.pair.v1", "pub": b64url(r.pcDER), "approval_pub": b64url(r.pcApprovalDER),
		"name": "tafs on this Linux PC", "platform": "linux", "kind": "hello", "nonce": b64url(nonce),
		"exp": r.now.Add(5 * time.Minute).Format(time.RFC3339)}
}

// send makes a request as id, with header as its signature when given.
func send(h http.Handler, id *auth.Identity, method, path, body, header string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if header != "" {
		req.Header.Set(auth.DeviceSignatureHeader, header)
	}
	if id != nil {
		h = withFakeIdentity(h, *id)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// approveAs sends qr to POST /v1/me/pairings as id, signed by key.
func (r *pairRig) approveAs(id auth.Identity, key *ecdsa.PrivateKey, qr map[string]any) (int, map[string]any) {
	b, _ := json.Marshal(qr)
	return send(r.authed, &id, http.MethodPost, "/v1/me/pairings", string(b), signRequest(r.t, key, id.Tenant, http.MethodPost, "/v1/me/pairings", string(b), r.now))
}

func (r *pairRig) approve(qr map[string]any) (int, map[string]any) {
	return r.approveAs(r.phone, r.approval, qr)
}

// claim polls POST /v1/pair/claim for nonce with pub, the proof signed by
// key, then moves the clock past the poll interval.
func (r *pairRig) claim(nonce string, pub []byte, key *ecdsa.PrivateKey) (int, map[string]any) {
	digest := sha256.Sum256(auth.PairClaimMessage(nonce, pub))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	b, _ := json.Marshal(map[string]string{"nonce": nonce, "pub": b64url(pub), "proof": base64.StdEncoding.EncodeToString(sig)})
	code, out := send(r.pub, nil, http.MethodPost, "/v1/pair/claim", string(b), "")
	r.now = r.now.Add(3 * time.Second)
	return code, out
}

// §5.3, §5.5: only the approval key of a device of the tenant approves a
// pairing; a window never covers it; a replayed request is refused.
func TestPairingNeedsThePhonesApprovalKey(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	if code, out := r.approveAs(r.phone, r.device, r.qr()); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("signed by the device key: %d %v", code, out)
	}
	legacy := r.phone
	legacy.ApprovalKey = nil
	if code, out := r.approveAs(legacy, r.device, r.qr()); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("a session with no approval key: %d %v", code, out)
	}
	browser := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin}
	if code, out := r.approveAs(browser, r.approval, r.qr()); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("a browser session: %d %v", code, out)
	}
	// An open window, and a tap signed inside it with the device key.
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "tenant-1", Subject: "user-abc", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: r.now, ExpiresAt: r.now.Add(5 * time.Minute), CapAt: r.now.Add(15 * time.Minute), OpenedBy: auth.DeviceKeyFingerprint(r.phone.ApprovalKey)}
	if err := r.windows.Open(context.Background(), win); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r.qr())
	tap := signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/me/pairings", string(b), r.now, win.ID)
	if code, out := send(r.authed, &r.phone, http.MethodPost, "/v1/me/pairings", string(b), tap); code != http.StatusForbidden || out["error"] != "critical_key_required" {
		t.Fatalf("a tap inside a window: %d %v", code, out)
	}
	header := signRequest(t, r.approval, "tenant-1", http.MethodPost, "/v1/me/pairings", string(b), r.now)
	if code, out := send(r.authed, &r.phone, http.MethodPost, "/v1/me/pairings", string(b), header); code != http.StatusCreated {
		t.Fatalf("signed by the approval key: %d %v", code, out)
	}
	if code, out := send(r.authed, &r.phone, http.MethodPost, "/v1/me/pairings", string(b), header); code != http.StatusConflict || out["error"] != "signature_replayed" {
		t.Fatalf("the same request again: %d %v", code, out)
	}
}

// §5.1, §5.5: every field within its bounds, or a refusal the phone shows.
func TestPairingFieldsAreChecked(t *testing.T) {
	t.Parallel()
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	for _, tc := range []struct {
		name   string
		change func(r *pairRig, q map[string]any)
		status int
		code   string
	}{
		{"name of 64 characters", func(r *pairRig, q map[string]any) { q["name"] = strings.Repeat("n", 64) }, http.StatusCreated, ""},
		{"macos", func(r *pairRig, q map[string]any) { q["platform"] = "macos"; q["kind"] = "enclave" }, http.StatusCreated, ""},
		{"another v", func(r *pairRig, q map[string]any) { q["v"] = "kiff.pair.v2" }, 400, "invalid_pairing"},
		{"kind passkey", func(r *pairRig, q map[string]any) { q["kind"] = "passkey"; delete(q, "approval_pub") }, 400, "invalid_pairing"},
		{"key_store", func(r *pairRig, q map[string]any) { q["key_store"] = "software" }, 400, "invalid_pairing"},
		{"unknown kind", func(r *pairRig, q map[string]any) { q["kind"] = "tpm" }, 400, "invalid_pairing"},
		{"a phone's platform", func(r *pairRig, q map[string]any) { q["platform"] = "ios" }, 400, "invalid_pairing"},
		{"no name", func(r *pairRig, q map[string]any) { q["name"] = " " }, 400, "invalid_pairing"},
		{"name of 65 characters", func(r *pairRig, q map[string]any) { q["name"] = strings.Repeat("n", 65) }, 400, "invalid_pairing"},
		{"name with a control character", func(r *pairRig, q map[string]any) { q["name"] = "tafs\non this PC" }, 400, "invalid_pairing"},
		{"pub in standard base64", func(r *pairRig, q map[string]any) { q["pub"] = base64.StdEncoding.EncodeToString(r.pcDER) }, 400, "invalid_pairing"},
		{"pub not P-256", func(r *pairRig, q map[string]any) { q["pub"] = b64url(p384DER) }, 400, "invalid_pairing"},
		{"approval_pub not P-256", func(r *pairRig, q map[string]any) { q["approval_pub"] = b64url(p384DER) }, 400, "invalid_pairing"},
		{"the two keys equal", func(r *pairRig, q map[string]any) { q["approval_pub"] = q["pub"] }, 400, "invalid_pairing"},
		{"hello without approval_pub", func(r *pairRig, q map[string]any) { delete(q, "approval_pub") }, 400, "invalid_pairing"},
		{"nonce of 31 bytes", func(r *pairRig, q map[string]any) { q["nonce"] = b64url(make([]byte, 31)) }, 400, "invalid_pairing"},
		{"nonce padded", func(r *pairRig, q map[string]any) { q["nonce"] = base64.URLEncoding.EncodeToString(make([]byte, 32)) }, 400, "invalid_pairing"},
		{"exp not RFC 3339", func(r *pairRig, q map[string]any) { q["exp"] = "in five minutes" }, 400, "invalid_pairing"},
		{"exp passed", func(r *pairRig, q map[string]any) { q["exp"] = r.now.Add(-time.Second).Format(time.RFC3339) }, 400, "pairing_expired"},
		{"exp past signed_at + 5 min", func(r *pairRig, q map[string]any) {
			q["exp"] = r.now.Add(5*time.Minute + time.Second).Format(time.RFC3339)
		}, 400, "invalid_pairing"},
		{"another field", func(r *pairRig, q map[string]any) { q["extra"] = true }, 400, "invalid_pairing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPairRig(t)
			q := r.qr()
			tc.change(r, q)
			code, out := r.approve(q)
			if code != tc.status || (tc.code != "" && (out["error"] != tc.code || out["words"] == "" || out["words"] == nil || out["message"] == nil)) {
				t.Fatalf("got %d %v, want %d %s with words", code, out, tc.status, tc.code)
			}
		})
	}
}

// §5.5: a nonce is approved once, and a key already a device's is refused.
func TestPairingNonceOnceAndKeysNew(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("first approval: %d %v", code, out)
	}
	r.now = r.now.Add(time.Second) // a new signature, not a replay
	if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "pairing_used" || out["words"] == nil {
		t.Fatalf("the nonce again: %d %v", code, out)
	}
	for _, which := range []string{"pub", "approval_pub"} {
		r := newPairRig(t)
		q := r.qr()
		key := r.pcDER
		if which == "approval_pub" {
			key = r.pcApprovalDER
		}
		// Another person's device on the account holds it.
		if err := r.devices.Insert(context.Background(), auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "tenant-1", Subject: "someone-else",
			Platform: "windows", PublicKey: key, CreatedAt: r.now}); err != nil {
			t.Fatal(err)
		}
		if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "key_registered" || out["words"] == nil {
			t.Fatalf("%s already a device's: %d %v", which, code, out)
		}
	}
}

// §5.2: the check code, against a vector computed outside Go:
// base32(sha256(pub_der ‖ approval_pub_der))[:6].
func TestPairingCheckCode(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	q := r.qr()
	// §2.4's documentation key, and a second fixed key.
	q["pub"] = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEuq6uAMg3330eLgKNwjg8RTxXLfC164Fzksg-3gI1gJsyV4iDPzJLgLSiMHvLSfKZg1w8QqHS5-XNYdilwVT9Yg"
	q["approval_pub"] = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE-zTMc3vmB-clPenvgNYgqlzdpeXQe-1md-jD5rK97EtI2pslRHbCBuVKADqCTgy_QZnxiszmHDmAOnTAi53yEA"
	code, out := r.approve(q)
	p, _ := out["pairing"].(map[string]any)
	if code != http.StatusCreated || p["code"] != "5SFGOB" || !strings.HasPrefix(p["id"].(string), "pair_") || p["expires"] != q["exp"] {
		t.Fatalf("approval: %d %v", code, out)
	}
}

// §5.4, §5.5: the computer claims the pairing once, proving it holds the
// approved device key, and its session is bound to the approved keys.
func TestPairingClaim(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	q := r.qr()
	nonce := q["nonce"].(string)
	if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
		t.Fatalf("before the phone approved: %d %v", code, out)
	}
	r.now = r.now.Add(-2 * time.Second) // 1 s after the last poll
	if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "slow_down" {
		t.Fatalf("polled within 3 s: %d %v", code, out)
	}
	q["exp"] = r.now.Add(4 * time.Minute).Format(time.RFC3339)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	// Another key, with its own valid proof: refused, and the pairing stays.
	otherKey, otherDER := p256(t)
	if code, out := r.claim(nonce, otherDER, otherKey); code != http.StatusForbidden || out["error"] != "invalid_proof" {
		t.Fatalf("another key's claim: %d %v", code, out)
	}
	// The approved pub, with a proof by another key.
	if code, out := r.claim(nonce, r.pcDER, otherKey); code != http.StatusForbidden || out["error"] != "invalid_proof" {
		t.Fatalf("a proof by another key: %d %v", code, out)
	}
	code, out := r.claim(nonce, r.pcDER, r.pcKey)
	if code != http.StatusOK || out["token_type"] != "Bearer" || out["tenant"] != "tenant-1" || out["subject"] != "user-abc" || out["expires_in"] == nil ||
		out["approval_mode"] != auth.ApprovalModeKey {
		t.Fatalf("the claim: %d %v", code, out)
	}
	parsed, err := auth.ParseDevSessionToken(out["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := r.sessions.GetByHash(context.Background(), parsed.Hash, r.now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sess.DevicePublicKey, r.pcDER) || !bytes.Equal(sess.ApprovalPublicKey, r.pcApprovalDER) || sess.DeviceID != out["device_id"] ||
		sess.Tenant != "tenant-1" || sess.Subject != "user-abc" {
		t.Fatalf("the session isn't bound to the approved keys: %+v", sess)
	}
	// It approves with its approval key, and its device counts as one that
	// approves (key_store "": a hello computer's key is in hardware).
	if pc, err := r.devices.Get(context.Background(), sess.DeviceID); sess.ApprovalMode != auth.ApprovalModeKey || err != nil || !pc.HasApprovalKey || pc.KeyStore != "" {
		t.Fatalf("the claimed session's mode %q, device %+v %v", sess.ApprovalMode, pc, err)
	}
	_, list := send(r.authed, &r.phone, http.MethodGet, "/v1/me/devices", "", "")
	devices, _ := list["devices"].([]any)
	found := false
	for _, d := range devices {
		d := d.(map[string]any)
		found = found || d["id"] == out["device_id"] && d["platform"] == "linux" && d["name"] == "tafs on this Linux PC"
	}
	if !found {
		t.Fatalf("the computer isn't in the person's devices: %v", list)
	}
	if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "expired_token" {
		t.Fatalf("a second claim: %d %v", code, out)
	}

	// A pairing whose exp passed can't be claimed.
	late := newPairRig(t)
	lq := late.qr()
	if code, out := late.approve(lq); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	late.now = late.now.Add(5*time.Minute + time.Second)
	if code, out := late.claim(lq["nonce"].(string), late.pcDER, late.pcKey); code != http.StatusBadRequest || out["error"] != "expired_token" {
		t.Fatalf("a claim after exp: %d %v", code, out)
	}
}

// A stolen phone approves a computer, and its owner removes the phone before
// the computer claims: the claim is refused, and the pairing is gone.
func TestPairingClaimRefusedOnceTheApprovingPhoneIsRemoved(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	ctx := context.Background()
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	if err := r.devices.Revoke(ctx, r.phone.DeviceID, r.now); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(10 * time.Second)
	if code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey); code != http.StatusForbidden || out["error"] != "approver_removed" {
		t.Fatalf("a removed phone's pairing was claimed: %d %v", code, out)
	}
}

// Removing a device takes back the pairings it approved that no computer claimed.
func TestRemovingADeviceDropsItsUnclaimedPairings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := auth.NewMemoryPairingStore()
	nonce := sha256.Sum256([]byte("n"))
	if err := store.Insert(ctx, auth.Pairing{ID: "pair_a", ApprovedBy: "dev_phone", NonceHash: nonce[:], Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.DropUnclaimedBy(ctx, "dev_phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ByNonce(ctx, nonce[:]); !errors.Is(err, auth.ErrPairingNotFound) {
		t.Fatalf("an unclaimed pairing outlived its approver: %v", err)
	}
}

// Someone removed from the account between the approval and the claim never gets a session.
func TestPairingClaimRefusedOnceThePersonIsNoLongerAMember(t *testing.T) {
	t.Parallel()
	r := newPairRig(t, handlers.WithDevicePairingMembers(membership.NewMemoryStore()))
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	r.now = r.now.Add(10 * time.Second)
	if code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey); code != http.StatusForbidden || out["error"] != "member_removed" {
		t.Fatalf("a removed member's pairing was claimed: %d %v", code, out)
	}
}

// §5.8: a new phone or tablet pairs from the person's phone as a computer
// does: it shows the QR (its platform, enclave or keystore, its device
// kind), the phone approves it with its approval key, and the claim gives a
// session held to the new device's approval key, listed as what it is.
func TestAPhonePairsFromAnotherPhone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ platform, kind, deviceKind, listed string }{
		{"ios", "enclave", "iphone", "iphone"},
		{"ipados", "enclave", "ipad", "ipad"},
		{"android", "keystore", "android_tablet", "android_tablet"},
		{"android", "keystore", "", "android_phone"},
	} {
		t.Run(c.platform+"/"+c.listed, func(t *testing.T) {
			r := newPairRig(t)
			q := r.qr()
			q["platform"], q["kind"], q["name"] = c.platform, c.kind, "tafs on the new device"
			if c.deviceKind != "" {
				q["device_kind"] = c.deviceKind
			}
			if code, out := r.approve(q); code != http.StatusCreated {
				t.Fatalf("the phone approves the new device: %d %v", code, out)
			}
			code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
			if code != http.StatusOK {
				t.Fatalf("the claim: %d %v", code, out)
			}
			parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
			sess, err := r.sessions.GetByHash(context.Background(), parsed.Hash, r.now)
			if err != nil || sess.ApprovalMode != auth.ApprovalModeKey || !bytes.Equal(sess.ApprovalPublicKey, r.pcApprovalDER) {
				t.Fatalf("the new device's session: %+v %v", sess, err)
			}
			d, err := r.devices.Get(context.Background(), sess.DeviceID)
			if err != nil || d.Platform != c.platform || d.Kind != c.listed || !d.HasApprovalKey {
				t.Fatalf("the new device: %+v %v", d, err)
			}
		})
	}
	for _, c := range []struct {
		name   string
		change func(q map[string]any)
	}{
		{"an iPhone with kind hello", func(q map[string]any) { q["platform"], q["kind"] = "ios", "hello" }},
		{"an Android with enclave", func(q map[string]any) { q["platform"], q["kind"] = "android", "enclave" }},
		{"a phone with a passkey", func(q map[string]any) {
			q["platform"], q["kind"] = "ios", "passkey"
			delete(q, "approval_pub")
		}},
		{"a phone without an approval key", func(q map[string]any) { q["platform"], q["kind"] = "ios", "enclave"; delete(q, "approval_pub") }},
		{"a computer with keystore", func(q map[string]any) { q["kind"] = "keystore" }},
		{"an iPad called an iPhone", func(q map[string]any) { q["platform"], q["kind"], q["device_kind"] = "ipados", "enclave", "iphone" }},
		{"an Android called an iPad", func(q map[string]any) { q["platform"], q["kind"], q["device_kind"] = "android", "keystore", "ipad" }},
		{"a computer with a device kind", func(q map[string]any) { q["device_kind"] = "iphone" }},
		{"an unknown platform", func(q map[string]any) { q["platform"], q["kind"] = "watchos", "enclave" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newPairRig(t)
			q := r.qr()
			c.change(q)
			if code, out := r.approve(q); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
				t.Fatalf("got %d %v, want 400 invalid_pairing", code, out)
			}
		})
	}
}

// §5.4: a phone's approve that KIFF refuses for a valid nonce because the
// computer's key is already a device's (key_registered) is kept against the
// nonce, so the computer's claim answers pairing_refused with the reason
// instead of authorization_pending until its code expires.
func TestARefusedPairingSaysSoAtTheClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// holdKey makes the rig's computer key a device's on the account, as a
	// computer already paired is; it returns the device's id.
	holdKey := func(r *pairRig) string {
		id := auth.NewDeviceID()
		if err := r.devices.Insert(ctx, auth.ApprovalDevice{ID: id, Tenant: "tenant-1", Subject: "user-abc",
			Platform: "linux", PublicKey: r.pcDER, CreatedAt: r.now}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("key_registered", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		nonce := q["nonce"].(string)
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("before the phone answered: %d %v", code, out)
		}
		holdKey(r)
		if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("the approve: %d %v", code, out)
		}
		code, out := r.claim(nonce, r.pcDER, r.pcKey)
		if code != http.StatusForbidden || out["error"] != "pairing_refused" || out["reason"] != "key_registered" || out["message"] == nil {
			t.Fatalf("the claim after a refused approve: %d %v", code, out)
		}
		// It says so until the code's exp, and no longer.
		r.now = r.now.Add(4 * time.Minute)
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusForbidden || out["error"] != "pairing_refused" {
			t.Fatalf("within the code's exp: %d %v", code, out)
		}
		r.now = r.now.Add(time.Minute)
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("past the code's exp: %d %v", code, out)
		}
	})

	// The QR isn't signed by the computer: whoever sees it can approve its
	// nonce from their own account. None of that may tell the real computer
	// it's already paired.
	attacker := func(r *pairRig) (auth.Identity, *ecdsa.PrivateKey, []byte) {
		t.Helper()
		_, devDER := p256(t)
		appKey, appDER := p256(t)
		id := auth.Identity{Subject: "mallory", Tenant: "tenant-2", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
			DeviceKey: devDER, DeviceSessionID: "sess-mallory", DeviceID: "dev_mallory", ApprovalKey: appDER}
		if err := r.devices.Insert(ctx, auth.ApprovalDevice{ID: id.DeviceID, Tenant: id.Tenant, Subject: id.Subject,
			Name: "mallory's phone", Platform: "android", Kind: auth.DeviceOther, PublicKey: devDER, CreatedAt: r.now}); err != nil {
			t.Fatal(err)
		}
		return id, appKey, devDER
	}
	stillPairs := func(t *testing.T, r *pairRig, q map[string]any) {
		t.Helper()
		nonce := q["nonce"].(string)
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("the real computer's claim after another account's refused approve: %d %v", code, out)
		}
		r.now = r.now.Add(time.Second)
		if code, out := r.approve(q); code != http.StatusCreated {
			t.Fatalf("the person's own approve: %d %v", code, out)
		}
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusOK {
			t.Fatalf("the real computer's claim after its own approve: %d %v", code, out)
		}
	}

	t.Run("another account's registered key as pub", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		mallory, malloryApproval, malloryDevice := attacker(r)
		planted := maps.Clone(q)
		planted["pub"] = b64url(malloryDevice)
		if code, out := r.approveAs(mallory, malloryApproval, planted); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("mallory's approve: %d %v", code, out)
		}
		stillPairs(t, r, q)
	})

	t.Run("the real pub with another account's registered approval_pub", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		mallory, malloryApproval, malloryDevice := attacker(r)
		planted := maps.Clone(q)
		planted["approval_pub"] = b64url(malloryDevice)
		if code, out := r.approveAs(mallory, malloryApproval, planted); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("mallory's approve: %d %v", code, out)
		}
		stillPairs(t, r, q)
	})

	t.Run("the real pub with another account's passkey credential", func(t *testing.T) {
		t.Parallel()
		passkeys := auth.NewMemoryPasskeyStore()
		r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
		credKey := func() []byte { _, der := p256(t); return der }()
		mallory, malloryApproval, _ := attacker(r)
		if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: []byte("cred-mallory"), Tenant: mallory.Tenant, PublicKey: credKey},
			Subject: mallory.Subject, DeviceID: "dev_mallory_pc", CreatedAt: r.now}); err != nil {
			t.Fatal(err)
		}
		q := r.qr()
		delete(q, "approval_pub")
		q["kind"], q["platform"] = "passkey", "windows"
		planted := maps.Clone(q)
		planted["credential"] = map[string]string{"id": b64url([]byte("cred-mallory")), "public_key": b64url(credKey)}
		if code, out := r.approveAs(mallory, malloryApproval, planted); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("mallory's approve: %d %v", code, out)
		}
		stillPairs(t, r, q)
	})

	t.Run("a claim that doesn't prove the refused key", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		nonce := q["nonce"].(string)
		holdKey(r)
		if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("the approve: %d %v", code, out)
		}
		otherKey, otherDER := p256(t)
		// The refused pub, with a proof by another key.
		if code, out := r.claim(nonce, r.pcDER, otherKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("a bad proof: %d %v", code, out)
		}
		// Another key, with its own good proof.
		if code, out := r.claim(nonce, otherDER, otherKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("another key's claim: %d %v", code, out)
		}
		// The refused key, proven: told.
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusForbidden || out["error"] != "pairing_refused" {
			t.Fatalf("the refused computer's claim: %d %v", code, out)
		}
	})

	t.Run("a later good approve wins", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		nonce := q["nonce"].(string)
		held := holdKey(r)
		if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("the refused approve: %d %v", code, out)
		}
		// The person removes the old device, and the phone approves the same code again.
		if err := r.devices.Revoke(ctx, held, r.now); err != nil {
			t.Fatal(err)
		}
		r.now = r.now.Add(time.Second)
		if code, out := r.approve(q); code != http.StatusCreated {
			t.Fatalf("the approve after the old device went: %d %v", code, out)
		}
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusOK || out["token_type"] != "Bearer" {
			t.Fatalf("the claim after the good approve: %d %v", code, out)
		}
	})

	t.Run("pairing_used records nothing", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		nonce := q["nonce"].(string)
		if code, out := r.approve(q); code != http.StatusCreated {
			t.Fatalf("approval: %d %v", code, out)
		}
		r.now = r.now.Add(time.Second)
		if code, out := r.approve(q); code != http.StatusConflict || out["error"] != "pairing_used" {
			t.Fatalf("the same code again: %d %v", code, out)
		}
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusOK {
			t.Fatalf("the claim after pairing_used: %d %v", code, out)
		}
	})

	t.Run("another nonce is untouched", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		refused, fresh := r.qr(), r.qr()
		holdKey(r)
		if code, out := r.approve(refused); code != http.StatusConflict || out["error"] != "key_registered" {
			t.Fatalf("the refused approve: %d %v", code, out)
		}
		if code, out := r.claim(fresh["nonce"].(string), r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("another code's claim: %d %v", code, out)
		}
	})

	t.Run("a request refused before its nonce is read records nothing", func(t *testing.T) {
		t.Parallel()
		r := newPairRig(t)
		q := r.qr()
		nonce := q["nonce"].(string)
		holdKey(r)
		q["name"] = ""
		if code, out := r.approve(q); code != http.StatusBadRequest || out["error"] != "invalid_pairing" {
			t.Fatalf("an invalid approve: %d %v", code, out)
		}
		if code, out := r.claim(nonce, r.pcDER, r.pcKey); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
			t.Fatalf("the claim after an invalid approve: %d %v", code, out)
		}
	})
}
