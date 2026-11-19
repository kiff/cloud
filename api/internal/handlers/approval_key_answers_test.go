package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// kiff/tafs#233: approving a held call, or changing the Card so it fits,
// grants authority: once the session has an approval key, only its
// signature counts, and the receipt's proof names that key. Refusing
// takes authority away and keeps the device key.
func TestApprovingNeedsTheApprovalKey(t *testing.T) {
	f := newHoldFixture(t)
	deviceKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	deviceDER, _ := x509.MarshalPKIXPublicKey(&deviceKey.PublicKey)
	approvalKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	approvalDER, _ := x509.MarshalPKIXPublicKey(&approvalKey.PublicKey)
	phone := person("alice", membership.RoleAdmin)
	phone.Method, phone.DeviceKey, phone.DeviceSessionID, phone.ApprovalKey = auth.MethodDeveloper, deviceDER, "sess-phone", approvalDER
	signedAt := time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)
	answer := func(key *ecdsa.PrivateKey, id, verb string) (map[string]any, string) {
		e, err := f.store.Exception(context.Background(), "t1", id)
		if err != nil {
			t.Fatal(err)
		}
		a := auth.Answer{TenantID: "t1", ExceptionID: id, Answer: verb, AccountingVersion: e.AccountingVersion, SignedAt: signedAt}
		digest := sha256.Sum256(a.Message())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		value := base64.StdEncoding.EncodeToString(sig)
		return map[string]any{"signature": map[string]string{"signed_at": signedAt, "value": value}}, value
	}

	held := f.hold("agent://a")
	for _, verb := range []string{"approve-once", "change-card"} {
		body, _ := answer(deviceKey, held, verb)
		if rr := do(t, f.mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+held+"/"+verb, body); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "approval_key_required") {
			t.Errorf("%s signed by the device key: %d %s", verb, rr.Code, rr.Body.String())
		}
	}
	body, value := answer(approvalKey, held, "approve-once")
	if rr := do(t, f.mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", body); rr.Code != http.StatusOK {
		t.Fatalf("approve signed by the approval key: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{held})
	if d := got[held].Device; d == nil || string(d.PublicKey) != string(approvalDER) || d.Signature != value {
		t.Errorf("the proof names the approval key: %+v", d)
	}

	// Refusing keeps the device key.
	other := f.hold("agent://a")
	body, _ = answer(deviceKey, other, "reject")
	if rr := do(t, f.mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+other+"/reject", body); rr.Code != http.StatusOK {
		t.Fatalf("refusal signed by the device key: %d %s", rr.Code, rr.Body.String())
	}
}

// The desktop-client design, §2.6: a session that approves nothing (view-only,
// or a device key in software) answers approve-once and change-card with a
// refusal, whatever key signed them; it still refuses a held call. A phone
// from before approval modes with no approval key approves with its device
// key, as it always has.
func TestAViewOnlySessionNeverApprovesAHeldCall(t *testing.T) {
	f := newHoldFixture(t)
	deviceKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	deviceDER, _ := x509.MarshalPKIXPublicKey(&deviceKey.PublicKey)
	signedAt := time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)
	answer := func(id, verb string) map[string]any {
		e, err := f.store.Exception(context.Background(), "t1", id)
		if err != nil {
			t.Fatal(err)
		}
		a := auth.Answer{TenantID: "t1", ExceptionID: id, Answer: verb, AccountingVersion: e.AccountingVersion, SignedAt: signedAt}
		digest := sha256.Sum256(a.Message())
		sig, _ := ecdsa.SignASN1(rand.Reader, deviceKey, digest[:])
		return map[string]any{"signature": map[string]string{"signed_at": signedAt, "value": base64.StdEncoding.EncodeToString(sig)}}
	}
	phone := person("alice", membership.RoleAdmin)
	phone.Method, phone.DeviceKey, phone.DeviceSessionID = auth.MethodDeveloper, deviceDER, "sess-phone"
	view := phone
	view.ApprovalMode = auth.ApprovalModeView
	software := phone
	software.ApprovalMode, software.KeyStore = auth.ApprovalModeKey, auth.KeyStoreSoftware
	for name, id := range map[string]auth.Identity{"view-only": view, "software-keyed": software} {
		held := f.hold("agent://a")
		for _, verb := range []string{"approve-once", "change-card"} {
			if rr := do(t, f.mux, id, http.MethodPost, "/v1/me/card-exceptions/"+held+"/"+verb, answer(held, verb)); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "view_only") {
				t.Errorf("%s: %s signed by the device key: %d %s", name, verb, rr.Code, rr.Body.String())
			}
		}
		if rr := do(t, f.mux, id, http.MethodPost, "/v1/me/card-exceptions/"+held+"/reject", answer(held, "reject")); rr.Code != http.StatusOK {
			t.Errorf("%s: a refusal signed by the device key: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	// A session held to an approval key it doesn't have approves nothing.
	keyless := phone
	keyless.ApprovalMode = auth.ApprovalModeKey
	held := f.hold("agent://a")
	if rr := do(t, f.mux, keyless, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", answer(held, "approve-once")); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "approval_key_required") {
		t.Errorf("mode key with no approval key, signed by the device key: %d %s", rr.Code, rr.Body.String())
	}
	// The legacy phone: approve-once by its device key goes through.
	if rr := do(t, f.mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", answer(held, "approve-once")); rr.Code != http.StatusOK {
		t.Fatalf("a phone from before approval modes, signed by the device key: %d %s", rr.Code, rr.Body.String())
	}
}

// The desktop client contract §2.6: a Windows computer approves a held call,
// or changes the Card so it fits, only with its own passkey's assertion over
// the answer's message; never with its device key, a synced passkey, or
// another device's. It still refuses with its device key. The receipt's
// proof names the passkey.
func TestAComputersOwnPasskeyAnswersAHeldCall(t *testing.T) {
	f := newHoldFixture(t)
	passkeys := auth.NewMemoryPasskeyStore()
	f.h.passkeys, f.h.rp = passkeys, auth.KiffRelyingParty
	deviceKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	deviceDER, _ := x509.MarshalPKIXPublicKey(&deviceKey.PublicKey)
	helloKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	helloDER, _ := x509.MarshalPKIXPublicKey(&helloKey.PublicKey)
	credential := []byte("windows-hello-credential")
	if err := passkeys.Insert(context.Background(), auth.StoredPasskey{Passkey: auth.Passkey{ID: credential, Tenant: "t1", PublicKey: helloDER},
		Subject: "alice", DeviceID: "dev_pc"}); err != nil {
		t.Fatal(err)
	}
	pc := person("alice", membership.RoleAdmin)
	pc.Method, pc.DeviceKey, pc.DeviceSessionID, pc.DeviceID, pc.ApprovalMode = auth.MethodDeveloper, deviceDER, "sess-pc", "dev_pc", auth.ApprovalModePasskey
	signedAt := time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)
	message := func(id, verb string) []byte {
		e, err := f.store.Exception(context.Background(), "t1", id)
		if err != nil {
			t.Fatal(err)
		}
		return auth.Answer{TenantID: "t1", ExceptionID: id, Answer: verb, AccountingVersion: e.AccountingVersion, SignedAt: signedAt}.Message()
	}
	assertion := func(m []byte, flags byte) *auth.Assertion {
		digest := sha256.Sum256(m)
		client := []byte(`{"type":"webauthn.get","challenge":"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `","origin":"https://kiff.dev"}`)
		rp := sha256.Sum256([]byte("kiff.dev"))
		authData := append(append(append([]byte(nil), rp[:]...), flags), 0, 0, 0, 0)
		ch := sha256.Sum256(client)
		signed := sha256.Sum256(append(append([]byte(nil), authData...), ch[:]...))
		sig, _ := ecdsa.SignASN1(rand.Reader, helloKey, signed[:])
		e := base64.RawURLEncoding.EncodeToString
		return &auth.Assertion{CredentialID: e(credential), ClientDataJSON: e(client), AuthenticatorData: e(authData), Signature: e(sig)}
	}
	byPasskey := func(id, verb string, flags byte) map[string]any {
		return map[string]any{"signature": map[string]any{"signed_at": signedAt, "passkey": assertion(message(id, verb), flags)}}
	}
	byDeviceKey := func(id, verb string) map[string]any {
		digest := sha256.Sum256(message(id, verb))
		sig, _ := ecdsa.SignASN1(rand.Reader, deviceKey, digest[:])
		return map[string]any{"signature": map[string]string{"signed_at": signedAt, "value": base64.StdEncoding.EncodeToString(sig)}}
	}
	const uv = 0x01 | 0x04

	held := f.hold("agent://a")
	for _, c := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"signed by the device key", byDeviceKey(held, "approve-once"), "passkey_required"},
		{"user present, not verified", byPasskey(held, "approve-once", 0x01), "passkey_invalid"},
		{"a synced passkey", byPasskey(held, "approve-once", uv|0x08|0x10), "passkey_synced"},
		{"the assertion of another answer", byPasskey(held, "change-card", uv), "passkey_invalid"},
	} {
		if rr := do(t, f.mux, pc, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", c.body); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want 403 %s", c.name, rr.Code, rr.Body.String(), c.want)
		}
	}
	// Another device's session, with this computer's passkey.
	other := pc
	other.DeviceID = "dev_other"
	if rr := do(t, f.mux, other, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", byPasskey(held, "approve-once", uv)); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "passkey_not_this_device") {
		t.Errorf("another device's session: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.mux, pc, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", byPasskey(held, "approve-once", uv)); rr.Code != http.StatusOK {
		t.Fatalf("approved by the computer's passkey: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{held})
	if d := got[held].Device; d == nil || string(d.PublicKey) != string(helloDER) || d.KeyID != "dev_pc" || d.Signature == "" {
		t.Errorf("the proof names the computer's passkey: %+v", d)
	}
	// The same answer again: the hold's state refuses it.
	if rr := do(t, f.mux, pc, http.MethodPost, "/v1/me/card-exceptions/"+held+"/approve-once", byPasskey(held, "approve-once", uv)); rr.Code == http.StatusOK {
		t.Errorf("the same approval twice: %d", rr.Code)
	}
	// A refusal stays the device key's.
	refused := f.hold("agent://a")
	if rr := do(t, f.mux, pc, http.MethodPost, "/v1/me/card-exceptions/"+refused+"/reject", byDeviceKey(refused, "reject")); rr.Code != http.StatusOK {
		t.Fatalf("a refusal by the device key: %d %s", rr.Code, rr.Body.String())
	}
	// Once the computer is removed, its passkey answers nothing.
	if err := passkeys.RevokeForDevice(context.Background(), "dev_pc", time.Now()); err != nil {
		t.Fatal(err)
	}
	later := f.hold("agent://a")
	if rr := do(t, f.mux, pc, http.MethodPost, "/v1/me/card-exceptions/"+later+"/approve-once", byPasskey(later, "approve-once", uv)); rr.Code != http.StatusForbidden {
		t.Errorf("the removed computer's passkey: %d %s", rr.Code, rr.Body.String())
	}
}
