package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A Card grant names the resources it applies to.

func TestIssueWithResourceScope(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)

	card := func(resources map[string]any) map[string]any {
		return map[string]any{
			"id": "card-rs", "subject": "agent://a", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND", "resources": resources}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
		}
	}
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates",
		card(map[string]any{"parameter": "reason", "values": []string{"duplicate", "damaged"}}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue = %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"resources":{"parameter":"reason","values":["duplicate","damaged"]}`) {
		t.Fatalf("view should carry the scope: %s", rr.Body.String())
	}
	ms, _ := store.List(context.Background(), "t1")
	if len(ms) != 1 || ms[0].Grants[0].Resources == nil || len(ms[0].Grants[0].Resources.Values) != 2 {
		t.Fatalf("stored: %+v", ms)
	}

	for name, resources := range map[string]map[string]any{
		"numeric parameter": {"parameter": "amount", "values": []string{"5"}},
		"no values":         {"parameter": "reason", "values": []string{}},
		"unknown field":     {"parameter": "reason", "values": []string{"x"}, "pattern": "*"},
	} {
		if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card(resources)); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, rr.Code, rr.Body.String())
		}
	}
}

func TestStringParametersForResourceScopes(t *testing.T) {
	long := strings.Repeat("x", mandate.MaxResourceLength+1)
	got, unreadable := stringParameters(map[string]any{
		"Order_ID": "ord_1", "amount": float64(5), "note": long,
		"region": "eu", "REGION": "us",
	})
	if len(got) != 1 || got["order_id"] != "ord_1" {
		t.Fatalf("got %v: want only order_id, lowercased", got)
	}
	if len(unreadable) != 2 || !strings.HasPrefix(unreadable["note"], "sha256:") || !strings.HasPrefix(unreadable["region"], "sha256:") {
		t.Fatalf("unreadable = %v: a long value and a case collision are kept as digests", unreadable)
	}
	// a review: two different long values must not look the same.
	_, a := stringParameters(map[string]any{"order_id": long + "a"})
	_, b := stringParameters(map[string]any{"order_id": long + "b"})
	if a["order_id"] == b["order_id"] {
		t.Fatal("two different long values have the same digest")
	}
	if r, u := stringParameters(map[string]any{"amount": 1}); r != nil || u != nil {
		t.Fatal("no string parameters is nil")
	}
}

func TestGateRefusesAnUnreadableScopedValueAsInvalid(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeUnreadableResource, MandateID: "mdt-refunds",
		Reason: "order_id is too long, or sent more than once, to match this card's resources exactly"}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != string(outcomeInvalid) || len(resp.Reasons) != 1 || resp.Reasons[0] != "invalid_parameter" || resp.Exception != nil {
		t.Fatalf("response = %+v, refused = %v", resp, refused)
	}
}

func TestGatePassesResourcesAndRefusesOutsideTheScope(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{
		Outcome: mandate.OutcomeResourceOutside, MandateID: "mdt-refunds",
		Reason: `reason "fraud" is not one this card applies to`,
	}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if stub.lastReq.Resources["reason"] != "duplicate" {
		t.Fatalf("Resources = %+v", stub.lastReq.Resources)
	}
	if !refused || resp.Outcome != string(outcomeBlocked) || len(resp.Reasons) != 1 || resp.Reasons[0] != "mandate_resource_not_on_card" {
		t.Fatalf("response = %+v, refused = %v", resp, refused)
	}
	if !strings.Contains(resp.Message, "resource mandate mdt-refunds does not name") {
		t.Errorf("message = %s", resp.Message)
	}
}
