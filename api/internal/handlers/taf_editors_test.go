package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// An editor changes the Cards of the tafs they made, and no other taf's.
// Ed made Refunds, Ada made Bills; an admin changes
// either, and an agent that isn't a taf keeps the role's rule.
func TestAnEditorChangesOnlyTheCardsOfTafsTheyMade(t *testing.T) {
	ctx := context.Background()
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	made := agents.NewMemoryStore()
	for id, a := range map[string]agents.Agent{
		"refunds": {Kind: agents.KindTaf, CreatedBy: "ed"},
		"bills":   {Kind: agents.KindTaf, CreatedBy: "ada"},
		"crm-bot": {CreatedBy: "ada"},
	} {
		a.TenantID, a.ID, a.CreatedAt = "t1", id, time.Now()
		if err := made.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, holder := range []string{"refunds", "bills", "crm-bot"} {
		if err := store.Issue(ctx, mandate.Mandate{ID: "card-" + holder, TenantID: "t1", Subject: holder, ValidUntil: until}, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMandatesHandler(store, nil, WithMandatesAgents(made), WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	ed := auth.Identity{Subject: "ed", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleEditor}
	ada := auth.Identity{Subject: "ada", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin}

	for _, tc := range []struct {
		name   string
		id     auth.Identity
		method string
		path   string
		body   any
		want   int
	}{
		{"Ed names Bills' Card", ed, http.MethodPut, "/v1/me/mandates/card-bills/title", map[string]any{"title": "x"}, http.StatusForbidden},
		{"Ed moves Refunds' Card to Bills", ed, http.MethodPost, "/v1/me/mandates/card-refunds/move", map[string]any{"subject": "bills"}, http.StatusForbidden},
		{"Ed shares Refunds' Card with Bills", ed, http.MethodPut, "/v1/me/mandates/card-refunds/holders", map[string]any{"subjects": []string{"refunds", "bills"}, "expected": []string{"refunds"}}, http.StatusForbidden},
		{"Ed revokes Bills' Card", ed, http.MethodPost, "/v1/me/mandates/card-bills/revoke", nil, http.StatusForbidden},
		{"Ed names Refunds' Card", ed, http.MethodPut, "/v1/me/mandates/card-refunds/title", map[string]any{"title": "Refunds"}, http.StatusOK},
		{"Ed names a Card of an agent that isn't a taf", ed, http.MethodPut, "/v1/me/mandates/card-crm-bot/title", map[string]any{"title": "CRM"}, http.StatusOK},
		{"Ada revokes Bills' Card", ada, http.MethodPost, "/v1/me/mandates/card-bills/revoke", nil, http.StatusOK},
	} {
		if rr := do(t, mux, tc.id, tc.method, tc.path, tc.body); rr.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rr.Code, tc.want, rr.Body.String())
		}
	}
}
