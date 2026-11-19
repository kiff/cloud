package handlers

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// One fingerprint for a Coach install that brings a Card: the draft's
// confirm, signed by the approval key, names Coach's held install and
// answers it too. It takes only that install, still held at the version
// the phone signed, and never under a window's tap; any other hold is
// left as it was, and the draft is confirmed either way.
// Category: authority
// Attack: a confirm's one fingerprint approves another held call (a refund), a stale or answered one, another account's, or one under a tap
// Refused: only the account's held version_install at the signed accounting version, under the approval key; every other hold untouched
func TestADraftsConfirmAnswersOnlyItsHeldInstall(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	r.startRae()
	windows := auth.NewMemoryWindowStore()
	policed := auth.DefaultRoutePolicy().WithWindows(windows).WithClock(func() time.Time { return r.now }).Wrap(r.mux)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approvalKey, approvalDER := key()
	phone := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone",
		DeviceName: "Pixel", ApprovalKey: approvalDER}
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(approvalDER)}
	if err := windows.Open(t.Context(), win); err != nil {
		t.Fatal(err)
	}

	// Coach's Card asks the owner for every install, and for a refund.
	coach := mandate.Mandate{ID: "coach-version-install", TenantID: "t1", Subject: "coach", OnExceed: mandate.OnExceedOwner,
		Grants:     []mandate.Grant{{Action: "version_install"}, {Action: "refund"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 0, Window: mandate.WindowCalendarDay}}}
	if err := r.store.Issue(context.Background(), coach, "alice"); err != nil {
		t.Fatal(err)
	}
	held := func(tenant, proposal, action string) mandate.Exception {
		t.Helper()
		if tenant != "t1" {
			other := coach
			other.TenantID = tenant
			_ = r.store.Issue(context.Background(), other, "bob")
		}
		a := mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.now })
		d, err := a.Authorize(context.Background(), tenant, proposal, mandate.Request{Subject: "coach", Action: action})
		if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
			t.Fatalf("hold %s: %+v %v", action, d, err)
		}
		e, err := r.store.Exception(context.Background(), tenant, d.ExceptionID)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	status := func(tenant, id string) mandate.ExceptionStatus {
		t.Helper()
		e, err := r.store.Exception(context.Background(), tenant, id)
		if err != nil {
			t.Fatal(err)
		}
		return e.Status
	}
	drafted := func(max int) (string, string) {
		t.Helper()
		code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", max)}})
		if code != http.StatusCreated || d["status"] != "draft" {
			t.Fatalf("drafting %d: %d %v", max, code, d)
		}
		return d["id"].(string), d["version"].(string)
	}
	signedReq := func(draft string, body []byte, k *ecdsa.PrivateKey, window string) *http.Request {
		path := "/v1/me/agent-drafts/" + draft + "/confirm"
		ts := r.now.Format(time.RFC3339)
		digest := sha256.Sum256(auth.RequestMessageWindow("t1", http.MethodPost, path, body, ts, window))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		header := "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
		if window != "" {
			header = "t=" + ts + ",v2=" + base64.StdEncoding.EncodeToString(sig) + ",w=" + window
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, header)
		return req.WithContext(auth.WithIdentity(req.Context(), phone))
	}
	send := func(req *http.Request) (int, map[string]any) {
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	body := func(version string, hold map[string]any, as string) []byte {
		b := map[string]any{"version": version, "hold": hold}
		if as != "" {
			b["as"] = as
		}
		out, _ := json.Marshal(b)
		return out
	}
	holdOf := func(e mandate.Exception) map[string]any {
		return map[string]any{"id": e.ID, "accounting_version": e.AccountingVersion}
	}
	confirm := func(hold map[string]any, max int, k *ecdsa.PrivateKey, window string) map[string]any {
		t.Helper()
		draft, version := drafted(max)
		code, out := send(signedReq(draft, body(version, hold, ""), k, window))
		if code != http.StatusOK || out["status"] != "confirmed" {
			t.Fatalf("the draft wasn't confirmed: %d %v", code, out)
		}
		h, _ := out["hold"].(map[string]any)
		return h
	}

	// The approval key's confirm names Coach's held install: both go, and
	// the answer's proof is the confirm's own signature.
	install := held("t1", "p-install", "version_install")
	if h := confirm(holdOf(install), 30000, approvalKey, ""); h["status"] != "approved" || h["id"] != install.ID {
		t.Fatalf("the install's hold: %v", h)
	}
	if status("t1", install.ID) != mandate.ExceptionApproved {
		t.Fatalf("the install wasn't approved: %s", status("t1", install.ID))
	}
	answers, _ := r.members.HoldAnswers(context.Background(), "t1", []string{install.ID})
	if a := answers[install.ID]; a.Answer != "approve-once" || a.Device == nil || a.Device.Name != "Pixel" ||
		!bytes.Equal(a.Device.PublicKey, approvalDER) || !strings.HasPrefix(a.Device.Message, "kiff.request.v1\nt1\nPOST\n/v1/me/agent-drafts/") || a.Device.Signature == "" {
		t.Fatalf("the answer's receipt: %+v", a)
	}

	// Another held call, a refund, is never taken by a confirm.
	refund := held("t1", "p-refund", "refund")
	if h := confirm(holdOf(refund), 31000, approvalKey, ""); h["status"] != "not_this_install" {
		t.Fatalf("a confirm naming a held refund: %v", h)
	}
	if status("t1", refund.ID) != mandate.ExceptionHeld {
		t.Fatalf("the refund was answered: %s", status("t1", refund.ID))
	}

	// A stale accounting version, an answered hold, another account's.
	stale := held("t1", "p-stale", "version_install")
	if h := confirm(map[string]any{"id": stale.ID, "accounting_version": stale.AccountingVersion + 1}, 32000, approvalKey, ""); h["status"] != "answered" {
		t.Fatalf("a stale accounting version: %v", h)
	}
	if status("t1", stale.ID) != mandate.ExceptionHeld {
		t.Fatalf("the stale hold was answered: %s", status("t1", stale.ID))
	}
	if h := confirm(holdOf(install), 33000, approvalKey, ""); h["status"] != "answered" {
		t.Fatalf("an answered hold: %v", h)
	}
	theirs := held("t2", "p-theirs", "version_install")
	if h := confirm(holdOf(theirs), 34000, approvalKey, ""); h["status"] != "not_found" {
		t.Fatalf("another account's hold: %v", h)
	}
	if status("t2", theirs.ID) != mandate.ExceptionHeld {
		t.Fatalf("another account's hold was answered: %s", status("t2", theirs.ID))
	}

	// A tap inside the window confirms a narrowing draft, and takes no hold.
	tapped := held("t1", "p-tap", "version_install")
	if h := confirm(holdOf(tapped), 10000, deviceKey, win.ID); h["status"] != "approval_key_required" {
		t.Fatalf("a window's tap: %v", h)
	}
	if status("t1", tapped.ID) != mandate.ExceptionHeld {
		t.Fatalf("a tap answered the hold: %s", status("t1", tapped.ID))
	}

	// A signed body can't be given another hold: the signature no longer
	// covers it, and nothing is confirmed or answered.
	draft, version := drafted(35000)
	req := signedReq(draft, body(version, holdOf(stale), ""), approvalKey, "")
	swapped := httptest.NewRequest(http.MethodPost, req.URL.Path, bytes.NewReader(body(version, holdOf(tapped), "")))
	swapped.Header = req.Header.Clone()
	if code, out := send(swapped.WithContext(req.Context())); code != http.StatusForbidden {
		t.Fatalf("a confirm with its hold swapped: %d %v", code, out)
	}
	if status("t1", tapped.ID) != mandate.ExceptionHeld || status("t1", stale.ID) != mandate.ExceptionHeld {
		t.Fatal("a swapped confirm answered a hold")
	}

	// A trial installs nothing: a hold with it is refused.
	draft, version = drafted(36000)
	if code, out := send(signedReq(draft, body(version, holdOf(tapped), asTrial), approvalKey, "")); code != http.StatusBadRequest {
		t.Fatalf("a trial naming a hold: %d %v", code, out)
	}
}

// A Windows computer's own passkey approves a draft's confirm, and the
// confirm answers Coach's held install as the phone's approval key does;
// the receipt's proof is the passkey's key and assertion.
func TestAComputersOwnPasskeyConfirmAnswersItsHeldInstall(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	r.startRae()
	passkeys := auth.NewMemoryPasskeyStore()
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return r.now }).WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(r.mux)
	deviceKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	deviceDER, _ := x509.MarshalPKIXPublicKey(&deviceKey.PublicKey)
	helloKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	helloDER, _ := x509.MarshalPKIXPublicKey(&helloKey.PublicKey)
	credential := []byte("windows-hello-1")
	if err := passkeys.Insert(context.Background(), auth.StoredPasskey{Passkey: auth.Passkey{ID: credential, Tenant: "t1", PublicKey: helloDER},
		Subject: "alice", DeviceID: "dev_pc"}); err != nil {
		t.Fatal(err)
	}
	pc := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-pc", DeviceID: "dev_pc",
		DeviceName: "tafs on this Windows PC", ApprovalMode: auth.ApprovalModePasskey}
	coach := mandate.Mandate{ID: "coach-version-install", TenantID: "t1", Subject: "coach", OnExceed: mandate.OnExceedOwner,
		Grants:     []mandate.Grant{{Action: "version_install"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 0, Window: mandate.WindowCalendarDay}}}
	if err := r.store.Issue(context.Background(), coach, "alice"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.now })
	d, err := a.Authorize(context.Background(), "t1", "p-install", mandate.Request{Subject: "coach", Action: "version_install"})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	install, _ := r.store.Exception(context.Background(), "t1", d.ExceptionID)
	code, drafted := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if code != http.StatusCreated {
		t.Fatalf("drafting: %d %v", code, drafted)
	}
	path := "/v1/me/agent-drafts/" + drafted["id"].(string) + "/confirm"
	body, _ := json.Marshal(map[string]any{"version": drafted["version"], "hold": map[string]any{"id": install.ID, "accounting_version": install.AccountingVersion}})
	ts := r.now.Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessage("t1", http.MethodPost, path, body, ts))
	client := []byte(`{"type":"webauthn.get","challenge":"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `","origin":"https://kiff.dev"}`)
	rp := sha256.Sum256([]byte("kiff.dev"))
	authData := append(append(append([]byte(nil), rp[:]...), 0x05), 0, 0, 0, 0)
	ch := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte(nil), authData...), ch[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, helloKey, signed[:])
	e := base64.RawURLEncoding.EncodeToString
	assertion, _ := json.Marshal(auth.Assertion{CredentialID: e(credential), ClientDataJSON: e(client), AuthenticatorData: e(authData), Signature: e(sig)})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(auth.PasskeyAssertionHeader, "t="+ts+",a="+e(assertion))
	rec := httptest.NewRecorder()
	policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), pc)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if h, _ := out["hold"].(map[string]any); rec.Code != http.StatusOK || out["status"] != "confirmed" || h["status"] != "approved" {
		t.Fatalf("the computer's confirm: %d %v", rec.Code, out)
	}
	answers, _ := r.members.HoldAnswers(context.Background(), "t1", []string{install.ID})
	if a := answers[install.ID]; a.Device == nil || !bytes.Equal(a.Device.PublicKey, helloDER) || a.Device.Signature != e(assertion) ||
		!strings.HasPrefix(a.Device.Message, "kiff.request.v1\nt1\nPOST\n/v1/me/agent-drafts/") {
		t.Fatalf("the answer's receipt names the computer's passkey: %+v", a.Device)
	}
}
