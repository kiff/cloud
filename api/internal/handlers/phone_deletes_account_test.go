package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The owner deletes the account from the app (kiff/tafs#185, App Store
// Review Guideline 5.1.1(v)). A phone's session has no sign-in to age, so
// the signature over the request, behind Face ID, is its recent sign-in
// (the governance-roles design, amended). Unsigned it's refused, signed it deletes, and a
// captured request replays to nothing.
func TestPhoneDeletesTheAccountOnlySigned(t *testing.T) {
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "phone-delete", Status: tenant.StatusActive}
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	// No fallback and no TrustAll: only a Clerk sign-in within the window
	// passes, as in production.
	stepUp := &auth.StepUp{Window: 10 * time.Minute}
	mux := http.NewServeMux()
	NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore()).WithStepUp(stepUp).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)

	p := newPhoneRig(t)
	admin := auth.Identity{Subject: "alice", Tenant: tnt.ID, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	phone := p.on(admin)
	path := "/v1/me/delete"
	status := func() tenant.Status {
		got, err := tenants.Get(context.Background(), tnt.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Status
	}

	if rr := serve(policed, phone, http.MethodPost, path, "", nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned: %d %s", rr.Code, rr.Body.String())
	}
	// The mark comes only from the route policy: the same session reaching
	// the handler any other way still needs a sign-in.
	if rr := serve(mux, phone, http.MethodPost, path, "", nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "reauth_required") {
		t.Fatalf("past the policy: %d %s", rr.Code, rr.Body.String())
	}
	// A dashboard session without a recent sign-in is refused as before.
	session := admin
	session.Method = auth.MethodSession
	if rr := serve(policed, session, http.MethodPost, path, "", nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "reauth_required") {
		t.Fatalf("a session without a recent sign-in: %d %s", rr.Code, rr.Body.String())
	}
	// Signed, an editor's phone still can't delete the account.
	ep := newPhoneRig(t)
	editor := ep.on(auth.Identity{Subject: "bob", Tenant: tnt.ID, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)})
	esig := map[string]string{auth.DeviceSignatureHeader: ep.signedRequest(tnt.ID, http.MethodPost, path, "", time.Now())}
	if rr := serve(policed, editor, http.MethodPost, path, "", esig); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "role_forbidden") {
		t.Fatalf("an editor's phone: %d %s", rr.Code, rr.Body.String())
	}
	if s := status(); s != tenant.StatusActive {
		t.Fatalf("deleted before a signed request: %s", s)
	}

	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest(tnt.ID, http.MethodPost, path, "", time.Now())}
	if rr := serve(policed, phone, http.MethodPost, path, "", sig); rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), "grace_window_ends") {
		t.Fatalf("signed: %d %s", rr.Code, rr.Body.String())
	}
	if s := status(); s != tenant.StatusDeletedPending {
		t.Fatalf("after the signed delete: %s", s)
	}
	if rr := serve(policed, phone, http.MethodPost, path, "", sig); rr.Code != http.StatusConflict {
		t.Fatalf("replayed: %d %s", rr.Code, rr.Body.String())
	}
}
