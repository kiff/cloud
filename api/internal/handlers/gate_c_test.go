package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// Invariant: a hard refusal has nothing to approve. A call past a Card set
// to refuse is refused with no held call behind it, so the owner's list shows
// nothing to answer, and approving it by its call's id finds nothing.
// Category: authority
// Attack: a call past a Card set to refuse, then an approval of that refusal, by the call's id or by guessing a held call's
// Refused: no held call is made, the owner's list is empty, and approve-once answers 404
func TestAHardRefusalHasNothingToApprove(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-hard", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedRefuse,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", "p-hard", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Allowed() || d.Outcome == mandate.OutcomeOwnerRequired || d.ExceptionID != "" {
		t.Fatalf("past a Card set to refuse: %+v %v", d, err)
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions?status=held", nil)
	var list struct {
		Exceptions []map[string]any `json:"exceptions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list.Exceptions) != 0 {
		t.Fatalf("held calls after a hard refusal: %s", rr.Body.String())
	}
	for _, id := range []string{"p-hard", "exc-p-hard"} {
		if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+id+"/approve-once", nil); rr.Code != http.StatusNotFound {
			t.Errorf("approving %s: %d, want 404", id, rr.Code)
		}
	}
	// Retried, it is refused again: nothing was approved.
	if again, err := a.Authorize(ctx, "t1", "p-hard", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}}); err != nil || again.Allowed() {
		t.Fatalf("retried: %+v %v", again, err)
	}
}

// Invariant: an account's drafts are its own. Another account's person can't
// read, confirm or discard one by its id.
// Category: isolation
// Attack: an owner of another account reads, confirms or discards a draft by its id
// Refused: each answers 404, and the draft is unchanged
func TestAnotherAccountCantReachADraft(t *testing.T) {
	r := newDraftRig(t)
	code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	id := d["id"].(string)
	read := func() string {
		rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+id, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("its own account reads it: %d", rr.Code)
		}
		return rr.Body.String()
	}
	before := read()
	other := auth.Identity{Subject: "mallory", Tenant: "t2", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin)}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/me/agent-drafts/" + id},
		{http.MethodPost, "/v1/me/agent-drafts/" + id + "/confirm"},
		{http.MethodDelete, "/v1/me/agent-drafts/" + id},
	} {
		if rr := do(t, r.mux, other, c.method, c.path, map[string]any{"version": d["version"]}); rr.Code != http.StatusNotFound {
			t.Errorf("another account's %s %s: %d %s", c.method, c.path, rr.Code, rr.Body.String())
		}
	}
	if after := read(); after != before {
		t.Fatalf("another account changed the draft:\n%s\n%s", before, after)
	}
}
