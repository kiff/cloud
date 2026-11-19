package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// Someone removed from the account leaves every group and grant, so
// coming back restores nothing; and someone who accepts an invitation
// pending when groups arrived joins Everyone, as they would have.
func TestMembersKeepGroupsInStep(t *testing.T) {
	ctx := context.Background()
	f := newMembersFixture(t)
	store := access.NewMemoryStore()
	f.h.access = store
	now := time.Now()
	if err := store.CreateGroup(ctx, access.Group{TenantID: "t1", ID: "everyone", Name: "Everyone", Everyone: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup(ctx, access.Group{TenantID: "t1", ID: "support", Name: "Support", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Bob's invitation was pending at the backfill; Carl's came after.
	bobTok := f.invite("bob@x.io", "approver")
	invites, err := f.svc.Store.Invites(ctx, "t1")
	if err != nil || len(invites) != 1 {
		t.Fatalf("invites: %v %v", invites, err)
	}
	store.Invites[[2]string{"t1", invites[0].ID}] = true
	carlTok := f.invite("carl@x.io", "viewer")
	for _, c := range []struct{ subject, tok string }{{"bob", bobTok}, {"carl", carlTok}} {
		if rr := do(t, f.personal, auth.Identity{Subject: c.subject, Method: auth.MethodSession}, http.MethodPost, "/v1/invites/accept",
			map[string]any{"token": c.tok}); rr.Code != http.StatusOK {
			t.Fatalf("%s accepts: %d %s", c.subject, rr.Code, rr.Body.String())
		}
	}
	everyone, _ := store.Members(ctx, "t1", "everyone")
	if len(everyone) != 1 || everyone[0].Subject != "bob" {
		t.Fatalf("Everyone after the invitations: %+v; only Bob's was pending at the backfill", everyone)
	}

	// Bob is shared a taf and joins Support; then he is removed, and comes back.
	if err := store.SetMember(ctx, access.GroupMember{TenantID: "t1", Group: "support", Subject: "bob", Role: access.Manager, AddedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddGrant(ctx, access.Grant{TenantID: "t1", Agent: "refunds", Subject: "bob", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, f.mux, session("alice", membership.RoleAdmin, true), http.MethodPost, "/v1/tenant/members/bob/remove", nil); rr.Code != http.StatusOK {
		t.Fatalf("remove Bob: %d %s", rr.Code, rr.Body.String())
	}
	if mine, _ := store.MembershipsOf(ctx, "t1", "bob"); len(mine) != 0 {
		t.Errorf("a removed member's groups: %+v", mine)
	}
	if grants, _ := store.Grants(ctx, "t1", "refunds"); len(grants) != 0 {
		t.Errorf("a removed member's grants: %+v", grants)
	}
	back := f.invite("bob@x.io", "approver")
	if rr := do(t, f.personal, auth.Identity{Subject: "bob", Method: auth.MethodSession}, http.MethodPost, "/v1/invites/accept",
		map[string]any{"token": back}); rr.Code != http.StatusOK {
		t.Fatalf("Bob comes back: %d %s", rr.Code, rr.Body.String())
	}
	m, err := f.svc.Store.Live(ctx, "t1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := store.MembershipsOf(ctx, "t1", "bob")
	grants, _ := store.Grants(ctx, "t1", "refunds")
	if a := access.Decide(&m, groups, grants); a.Use || a.Oversee {
		t.Errorf("Bob's access to Refunds after coming back: %+v", a)
	}
}
