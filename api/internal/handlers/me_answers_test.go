package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// fixedGovernance is an account's policy, as membership.Store reads it.
type fixedGovernance struct{ sod bool }

func (g fixedGovernance) Governance(_ context.Context, tenantID string) (membership.Governance, error) {
	return membership.Governance{TenantID: tenantID, SeparationOfDuties: g.sod}, nil
}

// /v1/me lists holds.answer only for a person who may answer under the
// account's separation of duties, as the hold gate decides: a phone then
// offers Approve only to whoever KIFF lets answer, and a test can read who
// may answer from a phone's session.
func TestMe_HoldsAnswerFollowsSeparationOfDuties(t *testing.T) {
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-sod", DisplayName: "Tenant SoD", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	me := func(sod bool, id auth.Identity) map[string]any {
		t.Helper()
		inner := http.NewServeMux()
		handlers.NewMeHandler(store, handlers.WithMeGovernance(fixedGovernance{sod})).Register(inner)
		id.Tenant = tnt.ID
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, id).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	answers := func(got map[string]any) bool {
		caps, _ := got["capabilities"].([]any)
		return slices.Contains(caps, any(string(membership.CapHoldsAnswer)))
	}
	person := func(role membership.Role) auth.Identity {
		return auth.Identity{Subject: "s-" + string(role), Method: auth.MethodDeveloper, MemberRole: role}
	}

	for _, c := range []struct {
		role    membership.Role
		sod     bool
		answers bool
	}{
		{membership.RoleAdmin, false, true},
		{membership.RoleEditor, false, true},
		{membership.RoleViewer, false, false},
		{membership.RoleAdmin, true, false},
		{membership.RoleEditor, true, false},
		{membership.RoleApprover, true, true},
	} {
		got := me(c.sod, person(c.role))
		if answers(got) != c.answers {
			t.Errorf("%s with separation of duties %v: holds.answer %v, want %v", c.role, c.sod, answers(got), c.answers)
		}
		if got["separation_of_duties"] != c.sod {
			t.Errorf("%s: separation_of_duties %v, want %v", c.role, got["separation_of_duties"], c.sod)
		}
		// Everything else a role holds is listed as before.
		if c.role == membership.RoleEditor && c.sod && !slices.Contains(got["capabilities"].([]any), any(string(membership.CapCardsManage))) {
			t.Errorf("editor under separation of duties lost cards.manage: %v", got["capabilities"])
		}
	}
	// A key carries no policy: it's a person's.
	key := me(true, auth.Identity{Subject: "k", Method: auth.MethodAPIKey, APIKey: true})
	if _, ok := key["separation_of_duties"]; ok {
		t.Errorf("a key's /v1/me has separation_of_duties: %v", key)
	}
}
