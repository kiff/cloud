package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// Group changes checked by KIFF itself, step one: adding someone to a group, or naming a manager, gives them the use of
// every agent shared with it, as sharing does. A phone's session signs it with its device key as before, or already
// with its approval key, and the membership trail says which one KIFF verified. The signature is checked before the
// group's rules: a request with none, or another key's, never reaches whether the person is on the account.
func TestAGroupAddTakesEitherKeyAndTheTrailSaysWhich(t *testing.T) {
	w := newAccessWorld(t)
	ada := person("ada", membership.RoleAdmin)
	code, out := w.do(ada, http.MethodPost, "/v1/groups", map[string]string{"name": "Support"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("make Support: %d %v", code, out)
	}
	group, _ := out["id"].(string)
	if group == "" {
		g, _ := out["group"].(map[string]any)
		group, _ = g["id"].(string)
	}
	deviceKey, deviceDER := p256(t)
	approvalKey, approvalDER := p256(t)
	otherKey, _ := p256(t)
	phone := ada
	phone.DeviceKey, phone.ApprovalKey, phone.DeviceSessionID = deviceDER, approvalDER, "sess-phone"
	old := ada
	old.DeviceKey, old.DeviceSessionID = deviceDER, "sess-old"

	n := 0
	put := func(id auth.Identity, key *ecdsa.PrivateKey, subject string) (int, string) {
		t.Helper()
		n++
		path := "/v1/groups/" + group + "/members/" + subject
		body := `{"role":"member"}`
		req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
		if key != nil {
			req.Header.Set(auth.DeviceSignatureHeader, signRequest(t, key, "t1", http.MethodPut, path, body, time.Now().Add(time.Duration(n)*time.Second)))
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(w.h, id).ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// Either key adds someone; another key, or none, adds no one.
	if code, body := put(phone, deviceKey, "ana"); code != http.StatusOK {
		t.Fatalf("added with the device key: %d %s", code, body)
	}
	if code, body := put(phone, approvalKey, "maria"); code != http.StatusOK {
		t.Fatalf("added with the approval key: %d %s", code, body)
	}
	if code, body := put(old, deviceKey, "paul"); code != http.StatusOK {
		t.Fatalf("added from a session without an approval key: %d %s", code, body)
	}
	for name, key := range map[string]*ecdsa.PrivateKey{"another key": otherKey, "no signature": nil} {
		if code, body := put(phone, key, "ed"); code != http.StatusForbidden || !strings.Contains(body, "signature_required") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// The signature comes first: someone who isn't on the account is refused for the signature, not looked up, when
	// it's missing; signed, the group's own rule answers.
	if code, body := put(phone, otherKey, "nobody"); code != http.StatusForbidden || !strings.Contains(body, "signature_required") {
		t.Errorf("unsigned, someone not on the account: %d %s", code, body)
	}
	if code, body := put(phone, approvalKey, "nobody"); code != http.StatusBadRequest || !strings.Contains(body, "not_a_member") {
		t.Errorf("signed, someone not on the account: %d %s", code, body)
	}

	// The trail says which key KIFF verified, never what the request claimed.
	trail, err := w.members.Audit(context.Background(), "t1", 50)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, e := range trail {
		if e.Action == "group.member_set" {
			by[e.Target] = e.AuthMethod
		}
	}
	want := map[string]string{"ana": string(auth.MethodSession) + "+device", "maria": string(auth.MethodSession) + "+critical", "paul": string(auth.MethodSession) + "+device"}
	for subject, method := range want {
		if by[subject] != method {
			t.Errorf("%s added by %q, want %q (trail %v)", subject, by[subject], method, by)
		}
	}

	// The other group writes are unchanged: the device key, never the approval key.
	n++
	path := "/v1/groups/" + group
	body := `{"name":"Help"}`
	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(body))
	req.Header.Set(auth.DeviceSignatureHeader, signRequest(t, approvalKey, "t1", http.MethodPatch, path, body, time.Now().Add(time.Duration(n)*time.Second)))
	rec := httptest.NewRecorder()
	withFakeIdentity(w.h, phone).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
		t.Errorf("a rename signed by the approval key: %d %s", rec.Code, rec.Body.String())
	}
}

// A computer that approves with its own passkey (Windows Hello, a security key) adds someone with that passkey's
// assertion over the exact request, as it shares a taf, and still with its device key until the switch. Another
// credential's assertion adds no one.
func TestAPasskeyComputerAddsToAGroupWithItsAssertion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys))
	hello := newWindowsHello(t)
	q := passkeyQR(r, hello)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approve: %d %v", code, out)
	}
	code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	if code != http.StatusOK {
		t.Fatalf("claim: %d %v", code, out)
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	sess, err := r.sessions.GetByHash(ctx, parsed.Hash, r.now)
	if err != nil {
		t.Fatal(err)
	}
	var with []auth.SignedWith
	r.inner.HandleFunc("PUT /v1/groups/{id}/members/{subject}", func(w http.ResponseWriter, req *http.Request) {
		got, _ := auth.SignedWithFrom(req.Context())
		with = append(with, got)
		w.WriteHeader(http.StatusOK)
	})
	authed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return r.now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(r.inner)
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: r.pcDER, DeviceSessionID: sess.ID, DeviceID: sess.DeviceID, ApprovalMode: auth.ApprovalModePasskey}
	const path, body = "/v1/groups/g1/members/u-2", `{"role":"member"}`
	n := 0
	call := func(header, value string) int {
		n++
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set(header, value)
		rec := httptest.NewRecorder()
		withFakeIdentity(authed, pc).ServeHTTP(rec, req)
		return rec.Code
	}
	at := func() time.Time { return r.now.Add(time.Duration(n+1) * time.Second) }
	if code := call(auth.PasskeyAssertionHeader, hello.header(t, "tenant-1", http.MethodPut, path, body, at(), "")); code != http.StatusOK {
		t.Fatalf("with the computer's passkey: %d", code)
	}
	if len(with) != 1 || len(with[0].Passkey) == 0 {
		t.Fatalf("signed with: %+v", with)
	}
	if code := call(auth.DeviceSignatureHeader, signRequest(t, r.pcKey, "tenant-1", http.MethodPut, path, body, at())); code != http.StatusOK {
		t.Fatalf("with the device key, until the switch: %d", code)
	}
	if len(with) != 2 || len(with[1].Passkey) != 0 || with[1].Key != auth.KeyDevice {
		t.Fatalf("signed with: %+v", with)
	}
	other := newWindowsHello(t)
	if code := call(auth.PasskeyAssertionHeader, other.header(t, "tenant-1", http.MethodPut, path, body, at(), "")); code == http.StatusOK || len(with) != 2 {
		t.Fatalf("another credential's assertion: %d, reached %d", code, len(with))
	}
}
