package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A refusal can carry a next step the agent reads.

func TestIssueWithNextSteps(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	card := func(steps []string) map[string]any {
		return map[string]any{
			"id": "card-ns", "subject": "agent://a", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND", "next_steps": steps}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
		}
	}
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card([]string{"return_first", "proof_first"}))
	if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"next_steps":["return_first","proof_first"]`) {
		t.Fatalf("issue = %d %s", rr.Code, rr.Body.String())
	}
	if ms, _ := store.List(context.Background(), "t1"); len(ms) != 1 || len(ms[0].Grants[0].NextSteps) != 2 {
		t.Fatalf("stored: %+v", ms)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card([]string{"Return First"})); rr.Code != http.StatusBadRequest {
		t.Fatalf("an invalid step: %d %s", rr.Code, rr.Body.String())
	}
}

func TestRejectWithNextStepReachesTheAgent(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "claude", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", NextSteps: []string{"return_first", "proof_first"},
			PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", "p-1", mandate.Request{Subject: "claude", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	path := "/v1/me/card-exceptions/" + d.ExceptionID

	// The owner is offered the Card's steps while the call is held.
	if rr := do(t, mux, owner(), http.MethodGet, path, nil); !strings.Contains(rr.Body.String(), `"next_steps":["return_first","proof_first"]`) {
		t.Fatalf("held view: %s", rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions?status=held", nil); !strings.Contains(rr.Body.String(), `"next_steps":["return_first","proof_first"]`) {
		t.Fatalf("held list: %s", rr.Body.String())
	}
	// A step the Card does not offer leaves the call held.
	if rr := do(t, mux, owner(), http.MethodPost, path+"/reject", map[string]any{"next_step": "refund_half"}); rr.Code != http.StatusBadRequest ||
		!strings.Contains(rr.Body.String(), "next_step_not_on_card") {
		t.Fatalf("unlisted step: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, path+"/reject", map[string]any{"next_step": "return_first", "surprise": true}); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, mux, owner(), http.MethodPost, path+"/reject", map[string]any{"next_step": "return_first", "note": "Send the return label first."})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"refusal":{"next_step":"return_first","note":"Send the return label first."}`) ||
		!strings.Contains(rr.Body.String(), `"status":"rejected"`) || strings.Contains(rr.Body.String(), `"next_steps"`) {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}

	// The agent reads what to do instead.
	rr = do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/holds?ids="+d.ExceptionID, nil)
	var got struct {
		Holds []map[string]any `json:"holds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got.Holds) != 1 {
		t.Fatalf("holds: %d %s", rr.Code, rr.Body.String())
	}
	if h := got.Holds[0]; h["status"] != "rejected" || h["next_step"] != "return_first" || h["note"] != "Send the return label first." {
		t.Fatalf("agent view: %+v", h)
	}
	if got, err := a.Authorize(ctx, "t1", "p-1", mandate.Request{Subject: "claude", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}}); err != nil || got.Outcome != mandate.OutcomeOwnerRejected {
		t.Fatalf("still refused: %+v %v", got, err)
	}
}

// An empty reject body is the plain refusal it always was.
func TestRejectWithoutBody(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "claude", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", NextSteps: []string{"return_first"},
			PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	d, _ := mandate.NewAuthorizer(store, nil, func() time.Time { return now }).Authorize(ctx, "t1", "p-1",
		mandate.Request{Subject: "claude", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/reject", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"refusal"`) {
		t.Fatalf("plain reject: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/holds?ids="+d.ExceptionID, nil)
	if strings.Contains(rr.Body.String(), "next_step") {
		t.Fatalf("a plain refusal carries no next step: %s", rr.Body.String())
	}
}

// An agent that retries a refused call learns the next step from the
// decide response itself, in fields and in the message.
func TestGateCarriesTheNextStepOnARejection(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerRejected, ExceptionID: "exc-1",
		Refusal: &mandate.Refusal{NextStep: "return_first", Note: "Label sent by email."}}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Reasons[0] != "mandate_owner_rejected" || resp.Exception == nil ||
		resp.Exception.NextStep != "return_first" || resp.Exception.Note != "Label sent by email." {
		t.Fatalf("response = %+v", resp)
	}
	if !strings.Contains(resp.Message, "next step return_first") || !strings.Contains(resp.Message, `"Label sent by email."`) {
		t.Fatalf("message = %s", resp.Message)
	}
	plain := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerRejected, ExceptionID: "exc-2"}}
	h = &ProposalsHandler{mandates: plain, logger: testLogger(), tenants: noRuntime{}}
	resp, _ = h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if resp.Exception.NextStep != "" || strings.Contains(resp.Message, "next step") {
		t.Fatalf("a plain refusal: %+v", resp)
	}
}
