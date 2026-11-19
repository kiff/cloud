package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

type trustedOutcomeFixture struct {
	evidence mandate.NoEffectEvidence
	found    bool
	err      error
}

func (f *trustedOutcomeFixture) Outcomes(context.Context, string, []string) (map[string]tooloutcome.Outcome, error) {
	return nil, nil
}

func (f *trustedOutcomeFixture) NoEffect(_ context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
	if tenant != f.evidence.TenantID || proposal != f.evidence.ProposalID {
		return mandate.NoEffectEvidence{}, false, nil
	}
	return f.evidence, f.found, f.err
}

func TestEffectReconciliationUsesTrustedEvidenceAndPreservesAttempts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s := mandate.NewMemoryStore()
	s.SetClock(func() time.Time { return now })
	m := mandate.Mandate{TenantID: "t1", ID: "effects", Subject: "agent-a", Grants: []mandate.Grant{{Action: "refund"}}, Aggregates: []mandate.Aggregate{
		{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100, Window: mandate.WindowCalendarDay},
		{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
	}}
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	if d, err := a.Authorize(ctx, "t1", "operation", mandate.Request{Subject: "agent-a", Action: "refund", Parameters: map[string]int64{"amount": 60}}); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	f := &trustedOutcomeFixture{evidence: mandate.NoEffectEvidence{TenantID: "t1", ProposalID: "operation", Subject: "agent-a", Action: "refund", At: now.Add(time.Second), Source: "trusted-executor", EvidenceID: "terminal-1", Reason: "final refusal"}}
	h := NewMandatesHandler(s, nil, WithMandatesToolOutcomes(f), WithMandatesClock(func() time.Time { return now.Add(2 * time.Second) }))
	mux := http.NewServeMux()
	h.Register(mux)
	path := "/v1/me/card-effects/operation/reconcile"
	agent := runtimeKey()
	agent.AgentID = "agent-a"
	// The caller's own error assertion is ignored, even with a bound key.
	if rr := do(t, mux, agent, http.MethodPost, path, map[string]any{"isError": true, "effect": "none"}); rr.Code != http.StatusConflict {
		t.Fatal(rr.Code, rr.Body.String())
	}
	f.found = true
	other := agent
	other.AgentID = "agent-b"
	if rr := do(t, mux, other, http.MethodPost, path, nil); rr.Code != http.StatusNotFound {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, runtimeKey(), http.MethodPost, path, nil); rr.Code != http.StatusForbidden {
		t.Fatal(rr.Code, rr.Body.String())
	}
	f.err = errors.New("unavailable")
	if rr := do(t, mux, agent, http.MethodPost, path, nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatal(rr.Code, rr.Body.String())
	}
	f.err = nil
	s.FailRecordWith(errors.New("write failed"))
	if rr := do(t, mux, agent, http.MethodPost, path, nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatal(rr.Code, rr.Body.String())
	}
	s.FailRecordWith(nil)
	for i := 0; i < 2; i++ {
		if rr := do(t, mux, agent, http.MethodPost, path, nil); rr.Code != http.StatusOK {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/effects/statement", nil)
	// Read the API's snake_case balance fields explicitly.
	var raw struct {
		Limits   []map[string]any        `json:"limits"`
		Releases []mandate.EffectRelease `json:"effect_releases"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &raw) != nil {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if len(raw.Releases) != 1 {
		t.Fatal(raw.Releases)
	}
	for _, l := range raw.Limits {
		if l["quantity"] == "count" && (l["used"] != float64(1) || l["released"] != float64(0)) {
			t.Fatal(l)
		}
		if l["quantity"] == "sum(amount)" && (l["used"] != float64(0) || l["gross_authorized"] != float64(60) || l["released"] != float64(60)) {
			t.Fatal(l)
		}
	}
}

func TestExtraRoomRequiresManagementAndDefaultsToThisWindow(t *testing.T) {
	mux, s := mandatesServer(t, owner())
	m := mandate.Mandate{TenantID: "t1", ID: "room", Subject: "agent-a", Grants: []mandate.Grant{{Action: "refund"}}, Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay}}}
	if err := s.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"revision_id": "owner-room", "quantity": "count", "window": "calendar_day", "measure": "attempts", "amount": 3}
	path := "/v1/me/mandates/room/extra-room"
	for _, id := range []auth.Identity{runtimeKey(), {Subject: "svc-agent", Tenant: "t1", AgentID: "agent-a", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"tenant_owner"}}} {
		if rr := do(t, mux, id, http.MethodPost, path, body); rr.Code != http.StatusForbidden {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
	if rr := do(t, mux, owner(), http.MethodPost, path, body); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	ms, err := s.ForSubject(context.Background(), "t1", "agent-a")
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	a := ms[0].Aggregates[0]
	if a.Limit != 10 || len(a.ExtraRoom) != 1 || a.ExtraRoom[0].Duration != mandate.RoomThisWindow || a.ExtraRoom[0].By != "alice" || a.ExtraRoom[0].ExpiresAt.Hour() != 0 {
		t.Fatal(a)
	}
	// A transport retry cannot issue a second grant or prolong the first.
	if rr := do(t, mux, owner(), http.MethodPost, path, body); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	revs, _ := s.Revisions(context.Background(), "t1", "room")
	if len(revs) != 2 {
		t.Fatal(revs)
	}
}
