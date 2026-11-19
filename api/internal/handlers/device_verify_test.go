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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// planque asks KIFF whether a person's device signed an approval, and
// whether that person may give it (kiff/planque#10): only with the
// account's connector key, only for an enrolled device of that account,
// and never for one of KIFF's own messages.
func TestADeviceSignatureIsVerifiedForAConnector(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	if _, err := svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	_, tok, err := svc.CreateInvite(ctx, "t1", membership.Actor{Subject: "alice"}, membership.InviteRequest{Email: "carol@x.io", Role: membership.RoleApprover})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "carol"}); err != nil {
		t.Fatal(err)
	}
	_, tok, err = svc.CreateInvite(ctx, "t1", membership.Actor{Subject: "alice"}, membership.InviteRequest{Email: "dave@x.io", Role: membership.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "dave"}); err != nil {
		t.Fatal(err)
	}

	devices := auth.NewMemoryDeviceStore()
	type device struct {
		id  string
		key *ecdsa.PrivateKey
	}
	enrol := func(tenant, subject string) device {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenant, Subject: subject, Name: "phone", Platform: "ios", PublicKey: der, CreatedAt: time.Now()}
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return device{d.ID, k}
	}
	sign := func(d device, msg string) string {
		digest := sha256.Sum256([]byte(msg))
		sig, _ := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	alicePhone, carolPhone, davePhone, otherAccount := enrol("t1", "alice"), enrol("t1", "carol"), enrol("t1", "dave"), enrol("t2", "bob")

	mux := http.NewServeMux()
	handlers.NewDeviceVerifyHandler(devices, svc).Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	ask := func(as auth.Identity, d device, msg, sig, capability string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"device_id": d.id, "message": base64.StdEncoding.EncodeToString([]byte(msg)), "signature": sig, "capability": capability})
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, as).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/devices/verify", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	msg := "planque.request.v1\nt1\nPOST\n/v1/accounts/t1/grants/g1/approve\nabc\n2026-10-19T09:00:00Z"

	if code, out := ask(connector, alicePhone, msg, sign(alicePhone, msg), "tools.connect"); code != http.StatusOK || out["valid"] != true ||
		out["device_id"] != alicePhone.id || out["name"] != "phone" || out["subject"] != "alice" || out["role"] != "admin" {
		t.Fatalf("the admin's signed approval: %d %v", code, out)
	}
	for name, c := range map[string]struct {
		d          device
		sig, cap   string
		wantReason string
	}{
		"an approver connecting tools": {carolPhone, sign(carolPhone, msg), "tools.connect", "not_allowed"},
		"another account's device":     {otherAccount, sign(otherAccount, msg), "tools.connect", "unknown_device"},
		"another device's signature":   {alicePhone, sign(carolPhone, msg), "tools.connect", "signature_invalid"},
		"an unknown device":            {device{id: "dev_nope", key: alicePhone.key}, sign(alicePhone, msg), "tools.connect", "unknown_device"},
	} {
		if code, out := ask(connector, c.d, msg, c.sig, c.cap); code != http.StatusOK || out["valid"] != false || out["reason"] != c.wantReason {
			t.Errorf("%s: %d %v; want %s", name, code, out, c.wantReason)
		}
	}
	// Erasing or exporting what planque keeps is the admin's, as deleting or
	// exporting the account is in KIFF (kiff/tafs#185, kiff/planque#46): an
	// editor connects tools, and nothing more.
	for _, c := range []string{"account.delete", "account.export"} {
		if code, out := ask(connector, alicePhone, msg, sign(alicePhone, msg), c); code != http.StatusOK || out["valid"] != true {
			t.Errorf("the admin, %s: %d %v", c, code, out)
		}
		if code, out := ask(connector, davePhone, msg, sign(davePhone, msg), c); code != http.StatusOK || out["valid"] != false || out["reason"] != "not_allowed" {
			t.Errorf("an editor, %s: %d %v", c, code, out)
		}
	}
	if code, out := ask(connector, davePhone, msg, sign(davePhone, msg), "tools.connect"); code != http.StatusOK || out["valid"] != true {
		t.Errorf("an editor connecting tools: %d %v", code, out)
	}
	// The approver may answer a hold, so that's valid for them.
	if code, out := ask(connector, carolPhone, msg, sign(carolPhone, msg), "holds.answer"); code != http.StatusOK || out["valid"] != true {
		t.Errorf("the approver answering: %d %v", code, out)
	}

	// Only a connector key asks, about a capability KIFF knows, never for one
	// of KIFF's own messages.
	for name, as := range map[string]auth.Identity{
		"a person":          {Tenant: "t1", Subject: "alice", Method: auth.MethodSession},
		"an agent's key":    {Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refunds"},
		"a phone's sign-in": {Tenant: "t1", Subject: "alice", Method: auth.MethodDeveloper},
	} {
		if code, _ := ask(as, alicePhone, msg, sign(alicePhone, msg), "tools.connect"); code != http.StatusForbidden {
			t.Errorf("%s verified a signature: %d", name, code)
		}
	}
	kiffMsg := "kiff.answer.v1\nt1\nexc_1\napprove-once\n\n\n1\n2026-10-19T09:00:00Z"
	if code, _ := ask(connector, alicePhone, kiffMsg, sign(alicePhone, kiffMsg), "holds.answer"); code != http.StatusBadRequest {
		t.Errorf("a kiff.* message was verified for a service: %d", code)
	}
	// One signature over two requests names the service's request too: the one kiff.* message it may bring.
	together := "kiff.requests.v1\nt1\n2026-10-19T09:00:00Z\n" + strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 64)
	if code, out := ask(connector, alicePhone, together, sign(alicePhone, together), "tools.connect"); code != http.StatusOK || out["valid"] != true {
		t.Errorf("a kiff.requests.v1 message: %d %v", code, out)
	}
	if code, _ := ask(connector, alicePhone, "kiff.requests.v10\n", sign(alicePhone, "kiff.requests.v10\n"), "tools.connect"); code != http.StatusBadRequest {
		t.Errorf("a kiff.* message that only starts like kiff.requests.v1: %d", code)
	}
	if code, _ := ask(connector, alicePhone, msg, sign(alicePhone, msg), "cards.manage"); code != http.StatusBadRequest {
		t.Errorf("an unlisted capability: %d", code)
	}

	// A removed device, or a person who left, approves nothing.
	if err := devices.Revoke(ctx, alicePhone.id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, out := ask(connector, alicePhone, msg, sign(alicePhone, msg), "tools.connect"); out["valid"] != false || out["reason"] != "unknown_device" {
		t.Errorf("a removed device: %v", out)
	}
	if _, err := svc.Remove(ctx, "t1", membership.Actor{Subject: "alice"}, "carol"); err != nil {
		t.Fatal(err)
	}
	if _, out := ask(connector, carolPhone, msg, sign(carolPhone, msg), "holds.answer"); out["valid"] != false || out["reason"] != "not_a_member" {
		t.Errorf("a person who left: %v", out)
	}
}
