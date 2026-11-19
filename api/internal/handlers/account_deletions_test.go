package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// countingSessions counts the times every phone of an account is signed out.
type countingSessions struct {
	auth.DeveloperSessionStore
	revoked map[string]int
}

func (c *countingSessions) RevokeAllForTenant(_ context.Context, t string, _ time.Time) error {
	c.revoked[t]++
	return nil
}

// Deleting the account from the app, driven by the server:
// the approval key's intent is accepted, deleting nothing and signing every
// phone out, then finished with only that intent, which needs no session.
func TestAnAccountIsDeletedFromItsSignedIntent(t *testing.T) {
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	t1, t2 := tenant.NewID(), tenant.NewID()
	for i, id := range []string{t1, t2} {
		if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: fmt.Sprintf("acme-%d", i), Status: tenant.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	keys := auth.NewMemoryAPIKeyStore()
	sessions := &countingSessions{revoked: map[string]int{}}
	h := NewAccountHandler(tenants, keys, nil).WithSessions(sessions)
	authed, public := http.NewServeMux(), http.NewServeMux()
	h.Register(authed)
	h.RegisterPublic(public)

	approval, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	approvalDER, _ := x509.MarshalPKIXPublicKey(&approval.PublicKey)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sign := func(k *ecdsa.PrivateKey, intent string) string {
		d := sha256.Sum256([]byte(intent))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, d[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	now := time.Now().UTC().Truncate(time.Second)
	intent := func(tenantID, device string, issued time.Time, ttl time.Duration, nonce string) string {
		return fmt.Sprintf("tafs.delete-account.v2\ntenant=%s\ndevice=%s\nissued=%s\nexpires=%s\nnonce=%s",
			tenantID, device, issued.Format(time.RFC3339), issued.Add(ttl).Format(time.RFC3339), nonce)
	}
	phone := func(role membership.Role) auth.Identity {
		return auth.Identity{Subject: "ada", Tenant: t1, Method: auth.MethodDeveloper, MemberRole: role,
			Roles: membership.IdentityRoles(role), DeviceID: "dev_1", DeviceKey: []byte{1}, ApprovalKey: approvalDER}
	}
	nonce := "0123456789abcdef0123456789abcdef"
	good := intent(t1, "dev_1", now, 10*time.Minute, nonce)
	accept := func(id auth.Identity, in, sig string) int {
		t.Helper()
		return do(t, authed, id, http.MethodPost, "/v1/account-deletions", map[string]string{"intent": in, "signature": sig}).Code
	}
	finish := func(in, sig string) int {
		t.Helper()
		return do(t, public, auth.Identity{}, http.MethodPost, "/v1/account-deletions/finish", map[string]string{"intent": in, "signature": sig}).Code
	}
	status := func(in, sig string) int {
		t.Helper()
		return do(t, public, auth.Identity{}, http.MethodPost, "/v1/account-deletions/status", map[string]string{"intent": in, "signature": sig}).Code
	}

	// Finishing, or asking about, what wasn't accepted is refused; without an
	// intent at all, unauthenticated.
	if code := finish(good, sign(approval, good)); code != http.StatusForbidden {
		t.Fatalf("finish before accept: %d", code)
	}
	if code := status(good, sign(approval, good)); code != http.StatusForbidden {
		t.Fatalf("status before accept: %d", code)
	}
	if code := status("", ""); code != http.StatusUnauthorized {
		t.Fatalf("status without an intent: %d", code)
	}
	// Each refusal at accept, and nothing is recorded or signed out.
	for name, c := range map[string]struct {
		id     auth.Identity
		in     string
		signer *ecdsa.PrivateKey
		want   int
	}{
		"expired":           {phone(membership.RoleAdmin), intent(t1, "dev_1", now.Add(-20*time.Minute), 10*time.Minute, nonce), approval, http.StatusBadRequest},
		"too long":          {phone(membership.RoleAdmin), intent(t1, "dev_1", now, time.Hour, nonce), approval, http.StatusBadRequest},
		"another account":   {phone(membership.RoleAdmin), intent(t2, "dev_1", now, 10*time.Minute, nonce), approval, http.StatusForbidden},
		"another device":    {phone(membership.RoleAdmin), intent(t1, "dev_2", now, 10*time.Minute, nonce), approval, http.StatusForbidden},
		"not an admin":      {phone(membership.RoleApprover), good, approval, http.StatusForbidden},
		"another key signs": {phone(membership.RoleAdmin), good, other, http.StatusForbidden},
	} {
		if code := accept(c.id, c.in, sign(c.signer, c.in)); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
	if sessions.revoked[t1] != 0 {
		t.Fatalf("a refused accept signed phones out")
	}
	// No approval key on the session: refused.
	noKey := phone(membership.RoleAdmin)
	noKey.ApprovalKey = nil
	if code := accept(noKey, good, sign(approval, good)); code != http.StatusForbidden {
		t.Errorf("without an approval key: %d", code)
	}

	// The account's keys: an agent's, and planque's connector key.
	for _, k := range []auth.APIKeyRecord{
		{ID: "k-agent", Tenant: t1, Subject: "ada", Roles: []string{"admin"}, AgentID: "refunds", KeyHash: [32]byte{7}, CreatedAt: now},
		{ID: "k-planque", Tenant: t1, Subject: "ada", Roles: []string{auth.RoleConnector}, KeyHash: [32]byte{8}, CreatedAt: now},
	} {
		if err := keys.Insert(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	// Accepted: nothing deleted, every phone signed out; again, the same.
	sig := sign(approval, good)
	for i := 0; i < 2; i++ {
		if code := accept(phone(membership.RoleAdmin), good, sig); code != http.StatusAccepted {
			t.Fatalf("accept %d: %d", i, code)
		}
	}
	if got, _ := tenants.Get(ctx, t1); got.Status != tenant.StatusActive || sessions.revoked[t1] != 1 {
		t.Fatalf("after accept: %s, phones signed out %d times", got.Status, sessions.revoked[t1])
	}
	// KIFF says it took the intent, and nothing is deleted by asking.
	if code := status(good, sig); code != http.StatusOK {
		t.Fatalf("status after accept: %d", code)
	}
	// Every key stops but planque's, which erases before KIFF finishes.
	active, _ := keys.ListActive(ctx, t1)
	if len(active) != 1 || active[0].ID != "k-planque" {
		t.Fatalf("keys after accept: %+v", active)
	}
	// Another intent, once one is accepted, is refused: KIFF finishes the first.
	second := intent(t1, "dev_1", now, 10*time.Minute, "fedcba9876543210fedcba9876543210")
	if code := accept(phone(membership.RoleAdmin), second, sign(approval, second)); code != http.StatusConflict {
		t.Fatalf("a second intent: %d, want 409", code)
	}
	// Finishing with another signature is refused; with the intent's own,
	// done, and done again, long after the intent expired.
	if code := finish(good, sign(other, good)); code != http.StatusForbidden {
		t.Errorf("finish with another key: %d", code)
	}
	for i := 0; i < 2; i++ {
		if code := finish(good, sig); code != http.StatusOK {
			t.Fatalf("finish %d: %d", i, code)
		}
	}
	if got, _ := tenants.Get(ctx, t1); got.Status != tenant.StatusDeletedPending {
		t.Fatalf("after finish: %s", got.Status)
	}
	if got, _ := tenants.Get(ctx, t2); got.Status != tenant.StatusActive {
		t.Fatalf("another account: %s", got.Status)
	}
}
