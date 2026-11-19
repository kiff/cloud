package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// the mandates design amendment, "the work comes home", at the API.

func policyOfCard(t *testing.T, store *mandate.MemoryStore, id string) string {
	t.Helper()
	ms, _ := store.List(context.Background(), "t1")
	for _, m := range ms {
		if m.ID == id {
			return m.OnExceed
		}
	}
	t.Fatalf("no card %s", id)
	return ""
}

// New Cards send work home by default; a reissue that does not name a
// policy keeps the Card's; only owner and refuse are accepted.
func TestIssuePolicy(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	body := func(policy string) map[string]any {
		b := map[string]any{
			"id": "card-p", "subject": "agent-a", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
		}
		if policy != "" {
			b["on_exceed"] = policy
		}
		return b
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("")); rr.Code != http.StatusCreated ||
		!strings.Contains(rr.Body.String(), `"on_exceed":"owner"`) {
		t.Fatalf("new card: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("refuse")); rr.Code != http.StatusCreated {
		t.Fatalf("explicit refuse: %d", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("")); rr.Code != http.StatusCreated {
		t.Fatalf("reissue: %d", rr.Code)
	}
	if got := policyOfCard(t, store, "card-p"); got != mandate.OnExceedRefuse {
		t.Fatalf("a reissue without on_exceed changed the policy to %q", got)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("sometimes")); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid policy: %d", rr.Code)
	}
}

// Answering is the owner's act: listed, approved once, refused twice,
// not reachable by the agent's own key or from another tenant.
func TestExceptionEndpoints(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", "p-1", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}

	if rr := do(t, mux, runtimeKey(), http.MethodGet, "/v1/me/card-exceptions", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("agent key listed exceptions: %d", rr.Code)
	}
	if rr := do(t, mux, runtimeKey(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/approve-once", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("agent key approved its own exception: %d", rr.Code)
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions?status=held", nil)
	var list struct {
		Exceptions []map[string]any `json:"exceptions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list.Exceptions) != 1 || list.Exceptions[0]["id"] != d.ExceptionID {
		t.Fatalf("list: %s", rr.Body.String())
	}
	other := auth.Identity{Subject: "mallory", Tenant: "t2", Roles: []string{"tenant_owner"}}
	if rr := do(t, mux, other, http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/approve-once", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("another tenant answered: %d", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/approve-once", nil); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), `"status":"approved"`) {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/change-card", nil); rr.Code != http.StatusConflict {
		t.Fatalf("second answer: %d", rr.Code)
	}
	if got, err := a.Authorize(ctx, "t1", "p-1", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}}); err != nil || got.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	st := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/card-x/statement", nil)
	if !strings.Contains(st.Body.String(), `"approved_outside":{"entries":[`) || !strings.Contains(st.Body.String(), `"totals":{"sum(amount)":500}`) {
		t.Fatalf("statement should total approved-outside separately: %s", st.Body.String())
	}
}

// The decide path maps the exception outcomes: owner_required with its
// handle, and a rejection as blocked with its own reason.
func TestGateMapsExceptionOutcomes(t *testing.T) {
	held := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1",
		Reason: "outside the card, held for its owner: mdt: over"}}
	h := &ProposalsHandler{mandates: held, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != "owner_required" || resp.Exception == nil || resp.Exception.ID != "exc-1" ||
		resp.Exception.Status != "held" || resp.Exception.ReviewURL != "/needs-you/exc-1" ||
		len(resp.Reasons) != 1 || resp.Reasons[0] != "mandate_owner_required" {
		t.Fatalf("owner_required: %+v", resp)
	}
	rejected := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerRejected, ExceptionID: "exc-1"}}
	h = &ProposalsHandler{mandates: rejected, logger: testLogger(), tenants: noRuntime{}}
	resp, refused = h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != string(outcomeBlocked) || resp.Reasons[0] != "mandate_owner_rejected" {
		t.Fatalf("owner_rejected: %+v", resp)
	}
}

// Observe mode reaches the Card check, so an action outside a Card is
// reported but not held.
func TestObservingReachesTheCardCheck(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeWithinLimit}}
	h := &ProposalsHandler{mandates: stub}
	ctx := withDecideMode(context.Background(), "observe")
	h.mandateRefusal(ctx, keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !stub.lastReq.Observe {
		t.Fatal("observe mode did not reach the card check")
	}
}

// A misspelled field is refused, never ignored: a Card sent with
// "per_action" instead of "per_action_max" was once issued with no
// per-call limit at all. Unknown fields anywhere in the body, and on a
// move, are refused the same way, and nothing is stored.
func TestCardBodyRejectsUnknownFields(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	card := func(grant map[string]any, agg map[string]any) map[string]any {
		return map[string]any{"id": "card-u", "subject": "agent-a", "domain": "refund-control",
			"grants": []map[string]any{grant}, "aggregates": []map[string]any{agg}}
	}
	agg := map[string]any{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}
	limit := []map[string]any{{"quantity": "sum(amount)", "max": 1000}}

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card(map[string]any{"action": "AUTO_REFUND", "per_action": limit}, agg))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `unknown field \"per_action\"`) ||
		!strings.Contains(rr.Body.String(), "per_action_max") {
		t.Fatalf("per_action typo: %d %s", rr.Code, rr.Body.String())
	}
	if ms, _ := store.List(context.Background(), "t1"); len(ms) != 0 {
		t.Fatalf("a Card was issued from a refused body: %+v", ms)
	}
	for label, body := range map[string]map[string]any{
		"aggregate": card(map[string]any{"action": "AUTO_REFUND"}, map[string]any{"quantity": "sum(amount)", "limit": 500000, "windw": "calendar_day"}),
		"top level": func() map[string]any {
			b := card(map[string]any{"action": "AUTO_REFUND"}, agg)
			b["onexceed"] = "refuse"
			return b
		}(),
		"per-action": card(map[string]any{"action": "AUTO_REFUND", "per_action_max": []map[string]any{{"quantity": "sum(amount)", "maximum": 1000}}}, agg),
	} {
		if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown field") {
			t.Errorf("%s: %d %s", label, rr.Code, rr.Body.String())
		}
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card(map[string]any{"action": "AUTO_REFUND", "per_action_max": limit}, agg)); rr.Code != http.StatusCreated {
		t.Fatalf("valid card: %d %s", rr.Code, rr.Body.String())
	}
	ms, _ := store.List(context.Background(), "t1")
	if len(ms) != 1 || len(ms[0].Grants[0].PerAction) != 1 || ms[0].Grants[0].PerAction[0].Max != 1000 {
		t.Fatalf("issued card lost its per-call limit: %+v", ms)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates/card-u/move", map[string]any{"subject": "agent-b", "reason": "x"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("move with an unknown field: %d %s", rr.Code, rr.Body.String())
	}
	if ms, _ := store.List(context.Background(), "t1"); ms[0].Subject != "agent-a" {
		t.Fatal("a refused move changed the holder")
	}
}
