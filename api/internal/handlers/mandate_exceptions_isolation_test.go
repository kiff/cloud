package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// Invariant: no key bound to an agent, and no runtime key, reads held work:
// not the list of card exceptions and not one by id, whoever's agent it
// belongs to or whichever Card it breached, shared or not. Held work
// carries the call's parameters and the Card's limits.
// Category: authority
// Attack: an agent's gateway or runtime key lists card exceptions, or reads another agent's held call by id
// Refused: reading held work needs the account roles or a person's membership; keys bound to an agent never hold either
func TestAgentKeysCannotReadHeldWork(t *testing.T) {
	r := newIsolationRig(t)
	at := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	expires := at.Add(24 * time.Hour)
	for _, e := range []mandate.Exception{
		{ID: "exc-a", ProposalID: "p-a", Subject: "agent-a", Breaches: []mandate.Breach{{MandateID: "card-a"}}},
		{ID: "exc-b", ProposalID: "p-b", Subject: "agent-b", Breaches: []mandate.Breach{{MandateID: "card-b"}}},
		{ID: "exc-shared", ProposalID: "p-s", Subject: "agent-b", Breaches: []mandate.Breach{{MandateID: "card-shared"}}},
	} {
		e.TenantID, e.Action, e.Status, e.CreatedAt, e.ExpiresAt = "acme", "refund", mandate.ExceptionHeld, at, &expires
		e.Request.Parameters = map[string]int64{"amount_cents": 99000}
		if err := r.store.SaveException(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{"exc-a", "exc-b", "exc-shared"}

	// Keys, through the auth middleware.
	for _, key := range []string{"gateway-a", "runtime-a", "runtime-b", "runtime-unbound"} {
		if code, body := r.get(t, key, "/v1/me/card-exceptions"); code != http.StatusForbidden {
			t.Errorf("%s list: %d, want 403 (%s)", key, code, body)
		}
		for _, id := range all {
			if code, body := r.get(t, key, "/v1/me/card-exceptions/"+id); code != http.StatusForbidden {
				t.Errorf("%s get %s: %d, want 403 (%s)", key, id, code, body)
			}
		}
	}
	code, body := r.get(t, "admin", "/v1/me/card-exceptions")
	if code != http.StatusOK || fmt.Sprint(exceptionIDs(t, body)) != fmt.Sprint(all) {
		t.Errorf("admin list: %d %s", code, body)
	}
	for _, id := range all {
		if code, body := r.get(t, "admin", "/v1/me/card-exceptions/"+id); code != http.StatusOK {
			t.Errorf("admin get %s: %d (%s)", id, code, body)
		}
	}

	// People read by their membership's role, which every role allows.
	// Sessions need the identity provider, so the identity is placed
	// below the middleware, as the middleware would place it.
	for _, role := range []membership.Role{membership.RoleAdmin, membership.RoleEditor, membership.RoleApprover, membership.RoleViewer} {
		person := auth.Identity{Subject: "person-" + string(role), Tenant: "acme", MemberRole: role,
			Roles: membership.IdentityRoles(role)}
		rr := do(t, r.mux, person, http.MethodGet, "/v1/me/card-exceptions", nil)
		if rr.Code != http.StatusOK || fmt.Sprint(exceptionIDs(t, rr.Body.Bytes())) != fmt.Sprint(all) {
			t.Errorf("%s list: %d %s", role, rr.Code, rr.Body.String())
		}
		if rr := do(t, r.mux, person, http.MethodGet, "/v1/me/card-exceptions/exc-b", nil); rr.Code != http.StatusOK {
			t.Errorf("%s get: %d %s", role, rr.Code, rr.Body.String())
		}
	}
}

func exceptionIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Exceptions []struct {
			ID string `json:"id"`
		} `json:"exceptions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(out.Exceptions))
	for _, e := range out.Exceptions {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
